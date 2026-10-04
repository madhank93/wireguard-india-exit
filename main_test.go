package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"gopkg.in/yaml.v3"
)

// RFC 7748 §6.1 Alice keypair, base64-encoded.
const (
	vectorPriv = "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo="
	vectorPub  = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
)

type mocks struct {
	mu       sync.Mutex
	userData string
	inbound  string
	fwFirst  bool
	sizes    []any
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	out := args.Inputs.Copy()
	switch args.TypeToken {
	case "random:index/randomBytes:RandomBytes":
		out["base64"] = resource.NewStringProperty(vectorPriv)
	case "digitalocean:index/firewall:Firewall":
		m.mu.Lock()
		m.inbound = fmt.Sprint(args.Inputs["inboundRules"])
		m.mu.Unlock()
	case "digitalocean:index/droplet:Droplet":
		m.mu.Lock()
		m.userData = args.Inputs["userData"].StringValue()
		m.fwFirst = m.inbound != ""
		m.mu.Unlock()
		out["ipv4Address"] = resource.NewStringProperty("203.0.113.1")
	}
	return "1", out, nil
}

func (m *mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if args.Token == "digitalocean:index/getSizes:getSizes" {
		return resource.NewPropertyMapFromMap(map[string]any{"sizes": m.sizes}), nil
	}
	return args.Args, nil
}

var availableSize = []any{map[string]any{"slug": size, "available": true, "regions": []any{region}}}

func TestPreflightRejectsUnavailableSize(t *testing.T) {
	err := pulumi.RunErr(program, pulumi.WithMocks("vpn", "test", &mocks{}))
	if err == nil || !strings.Contains(err.Error(), "not available in "+region) {
		t.Fatalf("want size-unavailable error, got %v", err)
	}
}

func TestProgram(t *testing.T) {
	m := &mocks{sizes: availableSize}
	if err := pulumi.RunErr(program, pulumi.WithMocks("vpn", "test", m)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.inbound, "51820") || strings.Count(m.inbound, "protocol") != 1 {
		t.Errorf("firewall must allow only UDP 51820 inbound, got %s", m.inbound)
	}
	if !m.fwFirst {
		t.Error("droplet was created before its firewall")
	}
	var cfg struct {
		WriteFiles []struct{ Path, Content string } `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(m.userData), &cfg); err != nil {
		t.Fatalf("user data is not valid YAML: %v", err)
	}
	var wg0 string
	for _, f := range cfg.WriteFiles {
		if f.Path == "/etc/wireguard/wg0.conf" {
			b, _ := base64.StdEncoding.DecodeString(f.Content)
			wg0 = string(b)
		}
	}
	for _, want := range []string{
		"PrivateKey = " + vectorPriv,
		"PublicKey = " + vectorPub + "\nAllowedIPs = 10.8.0.2/32",
		"PublicKey = " + vectorPub + "\nAllowedIPs = 10.8.0.3/32",
	} {
		if !strings.Contains(wg0, want) {
			t.Errorf("wg0.conf missing %q:\n%s", want, wg0)
		}
	}
}
