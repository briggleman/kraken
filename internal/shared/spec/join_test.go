package spec

import (
	"strings"
	"testing"
)

// joinSpec is validSpec with the settings and variables a join block names.
func joinSpec() *Spec {
	s := validSpec()
	s.Variables = append(s.Variables,
		Variable{Key: "SERVER_PASSWORD", Default: "", UserEditable: true},
		Variable{Key: "INVITE", Default: "", UserEditable: true},
	)
	s.Settings = Settings{Groups: []SettingGroup{{ID: "net", Fields: []SettingField{
		{Key: "ServerPassword", Type: FieldPassword},
		{Key: "AdminPassword", Type: FieldPassword},
		{Key: "RESTAPIPort", Type: FieldInt},
	}}}}
	return s
}

func TestJoin_Valid(t *testing.T) {
	for _, j := range []*Join{
		{Method: JoinAddress, Port: "game"},
		{Method: JoinAddress, Port: "game", Password: "SERVER_PASSWORD"},                // a variable
		{Method: JoinAddress, Port: "game", Password: "ServerPassword", Notes: "Join."}, // a setting
		{Method: JoinInviteCode, Code: "INVITE", Password: "ServerPassword"},
	} {
		s := joinSpec()
		s.Join = j
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: want valid, got %v", *j, err)
		}
	}
}

func TestJoin_Invalid(t *testing.T) {
	tests := []struct {
		name string
		join Join
		want string
	}{
		{"unknown method", Join{Method: "lobby"}, "join.method"},
		{"address without port", Join{Method: JoinAddress}, "join.port"},
		{"address with unknown port", Join{Method: JoinAddress, Port: "query"}, "not one of the spec's ports"},
		{"address with a code", Join{Method: JoinAddress, Port: "game", Code: "INVITE"}, "only for method invite_code"},
		{"invite without code", Join{Method: JoinInviteCode}, "join.code"},
		{"invite with undeclared code", Join{Method: JoinInviteCode, Code: "Nope"}, "not a variable or setting"},
		{"invite with a port", Join{Method: JoinInviteCode, Code: "INVITE", Port: "game"}, "only for method address"},
		{"undeclared password", Join{Method: JoinAddress, Port: "game", Password: "Typo"}, "not a variable or setting"},
		{"admin password", Join{Method: JoinAddress, Port: "game", Password: "AdminPassword"}, "admin credential"},
		{"admin code", Join{Method: JoinInviteCode, Code: "AdminPassword"}, "admin credential"},
		{"multi-line notes", Join{Method: JoinAddress, Port: "game", Notes: "one\ntwo"}, "one line"},
		{"long notes", Join{Method: JoinAddress, Port: "game", Notes: strings.Repeat("x", joinNotesMax+1)}, "one line"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := joinSpec()
			j := tc.join
			s.Join = &j
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The key a palworld-rest query reads as the admin password can never double as
// the join password, whatever it is called.
func TestJoin_QueryPasswordRefused(t *testing.T) {
	s := joinSpec()
	s.Query = &PlayerQuery{Method: "palworld-rest", Port: "RESTAPIPort", Password: "ServerPassword"}
	s.Join = &Join{Method: JoinAddress, Port: "game", Password: "ServerPassword"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "admin credential") {
		t.Fatalf("want admin-credential refusal, got %v", err)
	}
}

func TestAdminLike(t *testing.T) {
	for key, want := range map[string]bool{
		"AdminPassword": true, "adminPassword": true, "RCON_PASSWORD": true, "OwnerPassword": true,
		"ServerPassword": false, "SERVER_PASSWORD": false, "friendPassword": false, "WorldPassword": false,
		"game_password": false, "Password": false,
	} {
		if got := adminLike(key); got != want {
			t.Errorf("adminLike(%q) = %v, want %v", key, got, want)
		}
	}
}
