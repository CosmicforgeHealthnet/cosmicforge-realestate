package integration

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/migrate"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessInventoryMigrationUpgradesOnlyUnrestrictedInitialOwner(t *testing.T) {
	previous := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "00005" {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	db := testkit.NewDatabase(t, previous)
	owner, staff, org, role, limited, member, staffMember := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	for _, s := range []struct {
		q string
		a []any
	}{
		{`INSERT INTO business_accounts(id,email,display_name) VALUES($1,'owner@example.test','Owner'),($2,'staff@example.test','Staff')`, []any{owner, staff}},
		{`INSERT INTO organisations(id,representative_account_id,name) VALUES($1,$2,'Legacy business')`, []any{org, owner}},
		{`INSERT INTO role_templates(id,organisation_id,role_key,name,version) VALUES($1,$3,$1,'Owner',1),($2,$3,$2,'Limited',1)`, []any{role, limited, org}},
		{`INSERT INTO permissions(code,description) VALUES('roles:write','Roles'),('staff:read','Staff')`, nil},
		{`INSERT INTO role_permissions(role_id,permission) VALUES($1,'roles:write'),($2,'staff:read')`, []any{role, limited}},
		{`INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,kind,status,effective_at,scope_kind,scope_id) VALUES($1,$3,$4,$6,'OWNER','ACTIVE',now(),'COMPANY',$3),($2,$3,$5,$7,'EMPLOYEE','ACTIVE',now(),'COMPANY',$3)`, []any{member, staffMember, org, owner, staff, role, limited}},
		{`INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,'roles:write','COMPANY',$2,true,now())`, []any{ids.New(), org, member}},
	} {
		if _, err = db.Pool.Exec(t.Context(), s.q, s.a...); err != nil {
			t.Fatal(err)
		}
	}
	section4 := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() >= "00006" {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		section4[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	sqlDB, err := migrate.Open(db.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err = migrate.Up(t.Context(), sqlDB, section4); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context(), sqlDB, section4); err != nil {
		t.Fatal(err)
	}
	var next, unchanged ids.ID
	var version, caps, audits int
	if err = db.Pool.QueryRow(t.Context(), `SELECT m.role_id,r.version,(SELECT count(*) FROM role_permissions WHERE role_id=m.role_id AND permission LIKE 'inventory:%') FROM staff_memberships m JOIN role_templates r ON r.id=m.role_id WHERE m.id=$1`, member).Scan(&next, &version, &caps); err != nil {
		t.Fatal(err)
	}
	if next == role || version != 2 || caps != 5 {
		t.Fatalf("owner migration %v %d %d", next, version, caps)
	}
	if err = db.Pool.QueryRow(t.Context(), `SELECT role_id FROM staff_memberships WHERE id=$1`, staffMember).Scan(&unchanged); err != nil || unchanged != limited {
		t.Fatalf("limited role expanded %v %v", unchanged, err)
	}
	_ = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='SECTION4_OWNER_CAPABILITY_MIGRATION'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit %d", audits)
	}
}

func TestBusinessCommercialMigrationUpgradesOnlyUnrestrictedInitialOwner(t *testing.T) {
	previous := fstest.MapFS{}
	entries, e := fs.ReadDir(migrations.FS, ".")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "00006" {
			continue
		}
		raw, e := fs.ReadFile(migrations.FS, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	db := testkit.NewDatabase(t, previous)
	owner, staff, org, role, limited, member, staffMember := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	for _, item := range []struct {
		q string
		a []any
	}{
		{`INSERT INTO business_accounts(id,email,display_name) VALUES($1,'owner-upgrade@example.test','Owner'),($2,'staff-upgrade@example.test','Staff')`, []any{owner, staff}},
		{`INSERT INTO organisations(id,representative_account_id,name) VALUES($1,$2,'Legacy business')`, []any{org, owner}},
		{`INSERT INTO role_templates(id,organisation_id,role_key,name,version) VALUES($1,$3,$1,'Owner',2),($2,$3,$2,'Limited',1)`, []any{role, limited, org}},
		{`INSERT INTO permissions(code,description) VALUES('roles:write','Roles'),('staff:read','Staff') ON CONFLICT DO NOTHING`, nil},
		{`INSERT INTO role_permissions(role_id,permission) VALUES($1,'roles:write'),($2,'staff:read')`, []any{role, limited}},
		{`INSERT INTO staff_memberships(id,organisation_id,account_id,role_id,kind,status,effective_at,scope_kind,scope_id) VALUES($1,$3,$4,$6,'OWNER','ACTIVE',now(),'COMPANY',$3),($2,$3,$5,$7,'EMPLOYEE','ACTIVE',now(),'COMPANY',$3)`, []any{member, staffMember, org, owner, staff, role, limited}},
		{`INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,'roles:write','COMPANY',$2,true,now())`, []any{ids.New(), org, member}},
	} {
		if _, e = db.Pool.Exec(t.Context(), item.q, item.a...); e != nil {
			t.Fatal(e)
		}
	}
	sqlDB, e := migrate.Open(db.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	for range 2 {
		if e = migrate.Up(t.Context(), sqlDB, migrations.FS); e != nil {
			t.Fatal(e)
		}
	}
	var next, unchanged ids.ID
	var version, caps, audits int
	if e = db.Pool.QueryRow(t.Context(), `SELECT m.role_id,r.version,(SELECT count(*) FROM role_permissions WHERE role_id=m.role_id AND permission IN (SELECT code FROM permissions WHERE code LIKE 'channels:%' OR code LIKE 'rates:%')) FROM staff_memberships m JOIN role_templates r ON r.id=m.role_id WHERE m.id=$1`, member).Scan(&next, &version, &caps); e != nil || next == role || version != 3 || caps != 11 {
		t.Fatalf("Owner upgrade %s %d %d %v", next, version, caps, e)
	}
	if e = db.Pool.QueryRow(t.Context(), `SELECT role_id FROM staff_memberships WHERE id=$1`, staffMember).Scan(&unchanged); e != nil || unchanged != limited {
		t.Fatalf("Limited staff privileges expanded %s %v", unchanged, e)
	}
	if e = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='SECTIONS567_OWNER_CAPABILITY_MIGRATION'`).Scan(&audits); e != nil || audits != 1 {
		t.Fatalf("Missing immutable migration audit %d %v", audits, e)
	}
}
