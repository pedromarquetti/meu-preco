package nfce

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

type fakeSource struct{ payload string }

func (f *fakeSource) Fetch(_ context.Context, _ AccessKey, payload string) ([]byte, error) {
	f.payload = payload
	return []byte("<fake/>"), nil
}

type fakeParser struct{}

func (fakeParser) Parse(_ context.Context, _ AccessKey, _ []byte) (*domain.Nfce, error) {
	return &domain.Nfce{AccessKey: "x", Number: "1"}, nil
}

func TestMultiResolverRoutesToSupportedUF(t *testing.T) {
	src := &fakeSource{}
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(src, fakeParser{})})

	key, _ := ParseAccessKey(senateKey)
	n, err := m.Resolve(context.Background(), key, senateKey+"|3|1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n == nil || n.Number != "1" {
		t.Errorf("receipt = %+v", n)
	}
	if src.payload != senateKey+"|3|1" {
		t.Errorf("source payload = %q, want the forwarded payload", src.payload)
	}
}

func TestMultiResolverRejectsUnsupportedUF(t *testing.T) {
	m := NewMultiResolver(map[string]*Resolver{"35": NewResolver(&fakeSource{}, fakeParser{})})
	key, _ := ParseAccessKey("31240112345000123456650010000002571123456785") // MG, registered but unsupported

	_, err := m.Resolve(context.Background(), key, "")
	if !errors.Is(err, ErrUnsupportedUF) {
		t.Fatalf("resolve error = %v, want ErrUnsupportedUF", err)
	}
}

func TestMultiResolverEmptyTable(t *testing.T) {
	m := NewMultiResolver(nil)
	key, _ := ParseAccessKey(senateKey)
	if _, err := m.Resolve(context.Background(), key, ""); !errors.Is(err, ErrUnsupportedUF) {
		t.Fatalf("resolve error = %v, want ErrUnsupportedUF", err)
	}
}
