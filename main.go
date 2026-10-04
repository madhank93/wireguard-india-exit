package main

import (
	"crypto/ecdh"
	"embed"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/pulumi/pulumi-digitalocean/sdk/v4/go/digitalocean"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	region = "blr1"
	size   = "s-1vcpu-512mb-10gb"
	port   = 51820
)

// One WireGuard peer per device; peer i gets tunnel address 10.8.0.(i+2).
var clients = []string{"mac", "phone"}

//go:embed templates
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.tmpl"))

func render(name string, data any) (string, error) {
	var b strings.Builder
	err := templates.ExecuteTemplate(&b, name, data)
	return b.String(), err
}

type peer struct{ PublicKey, Address string }

func clientAddress(i int) string { return fmt.Sprintf("10.8.0.%d", i+2) }

// wgKey returns a WireGuard keypair whose private half lives in Pulumi state,
// so it stays stable across updates until the stack is destroyed.
func wgKey(ctx *pulumi.Context, name string) (priv, pub pulumi.StringOutput, err error) {
	b, err := random.NewRandomBytes(ctx, name, &random.RandomBytesArgs{Length: pulumi.Int(32)})
	if err != nil {
		return priv, pub, err
	}
	pub = b.Base64.ApplyT(func(s string) (string, error) {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return "", err
		}
		k, err := ecdh.X25519().NewPrivateKey(raw)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
	}).(pulumi.StringOutput)
	return b.Base64, pub, nil
}

// preflight queries the DigitalOcean API before any resource is touched, so a
// bad token fails fast (in preview too) and the size is confirmed for the region.
func preflight(ctx *pulumi.Context) error {
	res, err := digitalocean.GetSizes(ctx, &digitalocean.GetSizesArgs{
		Filters: []digitalocean.GetSizesFilter{{Key: "slug", Values: []string{size}}},
	})
	if err != nil {
		return fmt.Errorf("DigitalOcean API check failed (401: token invalid or expired, 403: token missing a scope): %w", err)
	}
	for _, s := range res.Sizes {
		if s.Available && slices.Contains(s.Regions, region) {
			return nil
		}
	}
	return fmt.Errorf("droplet size %s is not available in %s", size, region)
}

func main() {
	pulumi.Run(program)
}

func program(ctx *pulumi.Context) error {
	if err := preflight(ctx); err != nil {
		return err
	}
	serverPriv, serverPub, err := wgKey(ctx, "server-key")
	if err != nil {
		return err
	}
	var clientPrivs []pulumi.StringOutput
	serverInputs := []any{serverPriv}
	for _, c := range clients {
		priv, pub, err := wgKey(ctx, c+"-key")
		if err != nil {
			return err
		}
		clientPrivs = append(clientPrivs, priv)
		serverInputs = append(serverInputs, pub)
	}

	userData := pulumi.All(serverInputs...).ApplyT(func(v []any) (string, error) {
		var peers []peer
		for i, pub := range v[1:] {
			peers = append(peers, peer{pub.(string), clientAddress(i)})
		}
		conf, err := render("wg-server.conf.tmpl", struct {
			Port       int
			PrivateKey string
			Peers      []peer
		}{port, v[0].(string), peers})
		if err != nil {
			return "", err
		}
		return render("cloud-init.yaml.tmpl", struct{ ServerConfBase64 string }{
			base64.StdEncoding.EncodeToString([]byte(conf)),
		})
	}).(pulumi.StringOutput)

	// The firewall targets a tag and is created before the droplet, so the
	// droplet is never reachable on anything but WireGuard, even during boot.
	tag, err := digitalocean.NewTag(ctx, "vpn", &digitalocean.TagArgs{Name: pulumi.String("wg-vpn")})
	if err != nil {
		return err
	}
	anywhere := pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")}
	firewall, err := digitalocean.NewFirewall(ctx, "vpn", &digitalocean.FirewallArgs{
		Tags: pulumi.StringArray{tag.Name},
		InboundRules: digitalocean.FirewallInboundRuleArray{
			digitalocean.FirewallInboundRuleArgs{Protocol: pulumi.String("udp"), PortRange: pulumi.String(strconv.Itoa(port)), SourceAddresses: anywhere},
		},
		OutboundRules: digitalocean.FirewallOutboundRuleArray{
			digitalocean.FirewallOutboundRuleArgs{Protocol: pulumi.String("tcp"), PortRange: pulumi.String("1-65535"), DestinationAddresses: anywhere},
			digitalocean.FirewallOutboundRuleArgs{Protocol: pulumi.String("udp"), PortRange: pulumi.String("1-65535"), DestinationAddresses: anywhere},
			digitalocean.FirewallOutboundRuleArgs{Protocol: pulumi.String("icmp"), DestinationAddresses: anywhere},
		},
	})
	if err != nil {
		return err
	}

	droplet, err := digitalocean.NewDroplet(ctx, "vpn", &digitalocean.DropletArgs{
		Image:    pulumi.String("ubuntu-24-04-x64"),
		Region:   pulumi.String(region),
		Size:     pulumi.String(size),
		Tags:     pulumi.StringArray{tag.Name},
		UserData: userData.ToStringPtrOutput(),
	}, pulumi.DependsOn([]pulumi.Resource{firewall}))
	if err != nil {
		return err
	}

	ctx.Export("ip", droplet.Ipv4Address)
	for i, c := range clients {
		conf := pulumi.All(clientPrivs[i], serverPub, droplet.Ipv4Address).ApplyT(func(v []any) (string, error) {
			return render("wg-client.conf.tmpl", struct {
				PrivateKey, Address, ServerPublicKey, Endpoint string
				Port                                           int
			}{v[0].(string), clientAddress(i), v[1].(string), v[2].(string), port})
		}).(pulumi.StringOutput)
		ctx.Export(c, pulumi.ToSecret(conf))
	}
	return nil
}
