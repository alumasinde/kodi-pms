package rbac

import "testing"

func TestScopePropertyFilter(t *testing.T) {
	all := Scope{AllProperties: true}
	if frag, args := all.PropertyFilter("p.id"); frag != "1=1" || len(args) != 0 {
		t.Fatalf("all-properties filter = %q %v", frag, args)
	}

	none := Scope{}
	if frag, args := none.PropertyFilter("p.id"); frag != "1=0" || len(args) != 0 {
		t.Fatalf("empty filter = %q %v", frag, args)
	}

	some := Scope{PropertyIDs: []uint64{3, 7}}
	frag, args := some.PropertyFilter("p.id")
	if frag != "p.id IN (?,?)" || len(args) != 2 || args[0] != uint64(3) || args[1] != uint64(7) {
		t.Fatalf("property filter = %q %v", frag, args)
	}
}

func TestPrincipalCanAndRoles(t *testing.T) {
	p := &Principal{Perms: map[string]struct{}{"leases.view": {}}, Roles: []string{"property_manager"}}
	if !p.Can("leases.view") {
		t.Fatal("expected permission")
	}
	if p.Can("leases.activate") {
		t.Fatal("unexpected permission")
	}
	if !p.HasRole("property_manager") {
		t.Fatal("expected role")
	}
}
