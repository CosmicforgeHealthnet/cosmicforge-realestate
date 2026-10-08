package permissions

import "testing"

func TestRoleResolution(t *testing.T) {
	roles := RoleMap{
		"owner":  {"listing:create", "listing:publish"},
		"viewer": {"listing:read"},
	}
	s := roles.Resolve("owner", "viewer", "unknown")
	if !s.HasAll("listing:create", "listing:read") || s.Has("listing:delete") {
		t.Fatalf("resolved %v", s.List())
	}
	if !s.HasAny("listing:delete", "listing:read") || s.HasAny("x:y") {
		t.Fatal("HasAny")
	}
	if len(roles.Resolve("unknown")) != 0 {
		t.Fatal("unknown role must grant nothing")
	}
	if !Permission("listing:publish").Valid() || Permission("Listing:Publish").Valid() || Permission("nocolon").Valid() {
		t.Fatal("validity")
	}
}
