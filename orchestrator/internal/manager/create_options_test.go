package manager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

func TestReCreatedDatabaseKeepsItsJWTSecret(t *testing.T) {
	m := newInterpManager(t, reapStub{})
	old := strings.Repeat("ab", 32)
	_, wls, err := m.CreateProject(context.Background(), "demo", "", []WorkloadSpec{
		{Type: runtime.WorkloadTinbaseProject, Workspace: "db"},
		{Type: runtime.WorkloadRapidNativeDev, Name: "api", Workspace: "api"},
	}, CreateOptions{JWTSecret: old})
	if err != nil {
		t.Fatal(err)
	}
	if wls[0].JWTSecret != old {
		t.Fatalf("database secret %q, want the one passed", wls[0].JWTSecret)
	}
	if wls[1].JWTSecret == old {
		t.Fatal("a runner was given the database's secret")
	}
	if env := m.specFor(wls[0]).Env; env["TINBASE_JWT_SECRET"] != old {
		t.Fatalf("the database boots with TINBASE_JWT_SECRET %q", env["TINBASE_JWT_SECRET"])
	}
}

func TestShortJWTSecretIsRefused(t *testing.T) {
	m := newInterpManager(t, reapStub{})
	_, _, err := m.CreateProject(context.Background(), "demo", "", nil, CreateOptions{JWTSecret: "short"})
	if !errors.Is(err, ErrBadJWTSecret) {
		t.Fatalf("err = %v, want ErrBadJWTSecret", err)
	}
	if n := len(m.store.ListProjects()); n != 0 {
		t.Fatalf("a refused create left %d project(s)", n)
	}
}
