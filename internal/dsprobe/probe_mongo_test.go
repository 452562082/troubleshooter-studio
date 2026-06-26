package dsprobe

import "testing"

func TestMongoClientOptionsPreserveDirectConnectionQuery(t *testing.T) {
	opts, err := mongoClientOptionsFromFields(map[string]string{
		"uri": "mongodb://root:raw#pass@192.168.113.152:27018/admin?authSource=admin&directConnection=true&serverSelectionTimeoutMS=5000",
	})
	if err != nil {
		t.Fatalf("mongoClientOptionsFromFields returned error: %v", err)
	}

	if opts.Direct == nil || !*opts.Direct {
		t.Fatalf("directConnection=true was not preserved: %#v", opts.Direct)
	}
	if opts.Auth == nil {
		t.Fatalf("expected auth credentials")
	}
	if opts.Auth.Username != "root" {
		t.Fatalf("username mismatch: %q", opts.Auth.Username)
	}
	if opts.Auth.Password != "raw#pass" {
		t.Fatalf("raw password should be preserved, got %q", opts.Auth.Password)
	}
	if opts.Auth.AuthSource != "admin" {
		t.Fatalf("authSource mismatch: %q", opts.Auth.AuthSource)
	}
}
