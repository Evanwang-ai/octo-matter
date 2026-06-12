package notification

import "testing"

func TestCheckDelivered(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		target  string
		wantErr bool
	}{
		{"delivered (data envelope)", `{"data":{"delivered":["27A8InGz_bot"],"filtered":{}}}`, "27A8InGz_bot", false},
		{"delivered case-insensitive", `{"data":{"delivered":["27A8InGz_bot"],"filtered":{}}}`, "27a8ingz_bot", false},
		{"filtered not_space_member", `{"data":{"delivered":[],"filtered":{"ghost":"not_space_member"}}}`, "ghost", true},
		{"filtered send_failed", `{"data":{"delivered":[],"filtered":{"u1":"send_failed"}}}`, "u1", true},
		{"bare envelope delivered", `{"delivered":["u1"],"filtered":{}}`, "u1", false},
		{"bare envelope filtered", `{"delivered":[],"filtered":{"u1":"not_space_member"}}`, "u1", true},
		{"legacy empty body", ``, "u1", false},
		{"legacy non-json", `ok`, "u1", false},
		{"legacy json without shape", `{"status":"ok"}`, "u1", false},
		{"empty lists mean nobody got it", `{"data":{"delivered":[],"filtered":{}}}`, "u1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDelivered([]byte(c.body), c.target)
			if (err != nil) != c.wantErr {
				t.Fatalf("checkDelivered(%q, %q) err=%v, wantErr=%v", c.body, c.target, err, c.wantErr)
			}
		})
	}
}
