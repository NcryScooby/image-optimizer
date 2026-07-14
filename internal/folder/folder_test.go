package folder

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "catalog", in: "storely/1/catalog", want: "storely/1/catalog"},
		{name: "avatars", in: "storely/42/avatars", want: "storely/42/avatars"},
		{name: "themes", in: "storely/1/themes/abc-def", want: "storely/1/themes/abc-def"},
		{name: "panel admins", in: "storely/panel/admins/7", want: "storely/panel/admins/7"},
		{name: "trim slashes", in: "/storely/1/catalog/", want: "storely/1/catalog"},
		{name: "legacy nested catalog", in: "storely/1/catalog/extra", want: "storely/1/catalog/extra"},
		{name: "empty", in: "", wantErr: true},
		{name: "no storely prefix", in: "other/1/catalog", wantErr: true},
		{name: "traversal", in: "storely/../etc", wantErr: true},
		{name: "unknown kind", in: "storely/1/misc", wantErr: true},
		{name: "unsafe segment", in: "storely/1/cata log", wantErr: true},
		{name: "double slash", in: "storely//1/catalog", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate(%q) err = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q) unexpected err: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Validate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
