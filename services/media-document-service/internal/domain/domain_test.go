package domain

import (
	"strings"
	"testing"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func TestStateMachineIsExactlyTheApprovedModel(t *testing.T) {
	all := []Status{StatusInitiated, StatusUploadPending, StatusUploaded, StatusScanning, StatusReady, StatusRejected, StatusDeletePending, StatusDeleted}
	allowed := map[[2]Status]bool{
		{StatusInitiated, StatusUploadPending}: true, {StatusUploadPending, StatusUploaded}: true,
		{StatusUploaded, StatusScanning}: true, {StatusScanning, StatusReady}: true, {StatusScanning, StatusRejected}: true,
		{StatusReady, StatusDeletePending}: true, {StatusRejected, StatusDeletePending}: true, {StatusDeletePending, StatusDeleted}: true,
	}
	for _, f := range all {
		for _, to := range all {
			if Machine.Can(f, to) != allowed[[2]Status{f, to}] {
				t.Errorf("%s -> %s", f, to)
			}
		}
	}
	if !Machine.Terminal(StatusDeleted) || Machine.Can(StatusUploaded, StatusReady) {
		t.Fatal("an upload must never become READY without scanning")
	}
}

func TestClassPolicy(t *testing.T) {
	p := DefaultPolicies()
	cases := []struct {
		class Class
		mime  string
		name  string
		size  int64
		cls   Classification
		code  string
	}{
		{ClassImage, "image/jpeg", "house.JPG", 1000, Public, ""},
		{ClassImage, "application/x-msdownload", "setup.exe", 1000, Private, CodeMimeNotAllowed},
		{ClassImage, "image/png", "house.jpg", 1000, Private, CodeExtensionMismatch},
		{ClassImage, "image/png", "house.png", 16 << 20, Private, CodeTooLarge},
		{ClassImage, "image/png", "house.png", 0, Private, CodeTooLarge},
		{ClassDocument, "application/pdf", "deed.pdf", 1000, Public, CodeClassificationNotAllowed},
		{ClassVerificationDocument, "application/pdf", "id.pdf", 1000, Private, CodeClassificationNotAllowed},
		{ClassVerificationDocument, "application/pdf", "id.pdf", 1000, AdminRestricted, ""},
		{ClassDocument, "application/zip", "a.zip", 10, Private, CodeMimeNotAllowed},
	}
	for _, c := range cases {
		err := p[c.class].Validate(c.mime, c.name, c.size, c.cls)
		if apperrors.CodeOf(err) != c.code {
			t.Errorf("%s %s %s: got %v want %s", c.class, c.mime, c.name, err, c.code)
		}
	}
}

func TestSanitizeFilenameAndStorageKey(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":                "passwd",
		"C:\\Users\\x\\photo.jpg":         "photo.jpg",
		"we\"ird<na>me|.png":              "we_ird_na_me_.png",
		"...hidden.pdf":                   "hidden.pdf",
		"":                                "file",
		"line\nbreak.pdf":                 "line_break.pdf",
		strings.Repeat("a", 300) + ".pdf": strings.Repeat("a", 251) + ".pdf",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
	id := ids.New()
	k := StorageKey("artisan", ClassImage, id)
	if !strings.HasPrefix(k, "artisan/image/") || !strings.HasSuffix(k, id.String()) || strings.Contains(k, "..") {
		t.Fatal(k)
	}
}

func TestDetectMIME(t *testing.T) {
	cases := []struct {
		head []byte
		mime string
		exec bool
	}{
		{[]byte("%PDF-1.7\n"), "application/pdf", false},
		{[]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), "image/jpeg", false},
		{[]byte("\x89PNG\r\n\x1a\n\x00"), "image/png", false},
		{[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp", false},
		{[]byte("\x00\x00\x00\x18ftypheic"), "image/heic", false},
		{[]byte("\x00\x00\x00\x18ftypisom"), "video/mp4", false},
		{[]byte("MZ\x90\x00"), "application/x-executable", true},
		{[]byte("\x7fELF\x02"), "application/x-executable", true},
		{[]byte("#!/bin/sh\n"), "application/x-executable", true},
		{[]byte("PK\x03\x04"), "application/zip", true},
		{[]byte("hello"), "", false},
	}
	for _, c := range cases {
		m, e := DetectMIME(c.head)
		if m != c.mime || e != c.exec {
			t.Errorf("%q -> %s %v", c.head, m, e)
		}
	}
}

func TestAccessRules(t *testing.T) {
	user := ids.New()
	owner := Owner{Domain: platform.DomainIndividualOwner, Type: "USER", ID: user}
	ownerUser := Principal{Service: platform.APIGateway, ActorDomain: "individual-owner", ActorID: user}
	otherUser := Principal{Service: platform.APIGateway, ActorDomain: "individual-owner", ActorID: ids.New()}
	sameIDOtherDomain := Principal{Service: platform.APIGateway, ActorDomain: "artisan", ActorID: user}
	ownerSvc := Principal{Service: platform.IndividualOwnerService}
	otherSvc := Principal{Service: platform.MarketplaceService}
	admin := Principal{Service: platform.AdminService, ActorDomain: "admin", ActorID: ids.New()}
	adminNoActor := Principal{Service: platform.AdminService}
	verifier := Principal{Service: platform.VerificationService}

	type exp map[string]bool
	check := func(m MediaRef, grants []Grant, want exp) {
		t.Helper()
		ps := map[string]Principal{"ownerUser": ownerUser, "otherUser": otherUser, "sameIDOtherDomain": sameIDOtherDomain,
			"ownerSvc": ownerSvc, "otherSvc": otherSvc, "admin": admin, "adminNoActor": adminNoActor, "verifier": verifier}
		for name, p := range ps {
			if got := CanDownload(m, p, grants); got != want[name] {
				t.Errorf("%s/%s %s: got %v", m.Class, m.Classification, name, got)
			}
		}
	}
	all := exp{"ownerUser": true, "otherUser": true, "sameIDOtherDomain": true, "ownerSvc": true, "otherSvc": true, "admin": true, "adminNoActor": true, "verifier": true}
	check(MediaRef{Owner: owner, Class: ClassImage, Classification: Public}, nil, all)
	check(MediaRef{Owner: owner, Class: ClassImage, Classification: Private}, nil, exp{"ownerUser": true, "ownerSvc": true, "admin": true})
	check(MediaRef{Owner: owner, Class: ClassImage, Classification: Private}, []Grant{{Service: "marketplace-service"}},
		exp{"ownerUser": true, "ownerSvc": true, "admin": true, "otherSvc": true})
	check(MediaRef{Owner: owner, Class: ClassDocument, Classification: DomainRestricted}, nil, exp{"ownerSvc": true, "admin": true})
	check(MediaRef{Owner: owner, Class: ClassVerificationDocument, Classification: AdminRestricted}, nil, exp{"admin": true, "verifier": true})
	check(MediaRef{Owner: owner, Class: ClassDocument, Classification: AdminRestricted}, nil, exp{"admin": true})
	if !CanReadMetadata(MediaRef{Owner: owner, Class: ClassDocument, Classification: AdminRestricted}, verifier, nil) {
		t.Fatal("verification-service needs metadata for evidence checks")
	}
	if CanManage(MediaRef{Owner: owner}, otherSvc) || !CanManage(MediaRef{Owner: owner}, ownerSvc) || !CanManage(MediaRef{Owner: owner}, admin) || CanManage(MediaRef{Owner: owner}, adminNoActor) {
		t.Fatal("manage rules")
	}
}
