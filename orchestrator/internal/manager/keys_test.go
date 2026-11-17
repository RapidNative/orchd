package manager

import "testing"

// The well-known Supabase local demo keys: tinbase's deterministic keys with the
// default secret are byte-identical to them (tinbase src/jwt.ts), so these pin
// signTinbaseKeys to what `tinbase keys` prints.
const (
	demoSecret = "super-secret-jwt-token-with-at-least-32-characters-long"
	demoAnon   = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZS1kZW1vIiwicm9sZSI6ImFub24iLCJleHAiOjE5ODM4MTI5OTZ9.CRXP1A7WOeoJeXxjNni43kdQwgnWNReilDMblYTn_I0"
	demoSvc    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZS1kZW1vIiwicm9sZSI6InNlcnZpY2Vfcm9sZSIsImV4cCI6MTk4MzgxMjk5Nn0.EGIM96RAZx35lJzdJsyH-qQwv8Hdp7fsn3W0YpN81IU"
)

func TestSignTinbaseKeysMatchesTinbase(t *testing.T) {
	anon, svc := signTinbaseKeys(demoSecret)
	if anon != demoAnon {
		t.Errorf("anon key\n got %s\nwant %s", anon, demoAnon)
	}
	if svc != demoSvc {
		t.Errorf("service_role key\n got %s\nwant %s", svc, demoSvc)
	}
}

func TestMintKeysNeedsNoTinbase(t *testing.T) {
	m := &Manager{}
	anon, svc := m.mintKeys(demoSecret)
	if anon != demoAnon || svc != demoSvc {
		t.Fatalf("mintKeys = %q, %q", anon, svc)
	}
}
