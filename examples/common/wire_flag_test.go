package common

import "testing"

func strp(s string) *string { return &s }

func TestWireFlags_TokenResolution(t *testing.T) {
	cases := []struct {
		name                   string
		wf                     WireFlags
		wantServer, wantClient string
	}{
		{
			name:       "empty falls through both sides",
			wf:         WireFlags{Wire: strp(""), ServerWire: strp(""), ClientWire: strp("")},
			wantServer: "",
			wantClient: "",
		},
		{
			name:       "legacy pairs 1:1",
			wf:         WireFlags{Wire: strp("legacy"), ServerWire: strp(""), ClientWire: strp("")},
			wantServer: "legacy",
			wantClient: "legacy",
		},
		{
			name:       "stateless pairs 1:1",
			wf:         WireFlags{Wire: strp("stateless"), ServerWire: strp(""), ClientWire: strp("")},
			wantServer: "stateless",
			wantClient: "stateless",
		},
		{
			name:       "dual maps client to adaptive",
			wf:         WireFlags{Wire: strp("dual"), ServerWire: strp(""), ClientWire: strp("")},
			wantServer: "dual",
			wantClient: "adaptive",
		},
		{
			name:       "DUAL is case-insensitive for the client mapping",
			wf:         WireFlags{Wire: strp("DUAL"), ServerWire: strp(""), ClientWire: strp("")},
			wantServer: "DUAL",
			wantClient: "adaptive",
		},
		{
			name:       "server override beats primary",
			wf:         WireFlags{Wire: strp("stateless"), ServerWire: strp("legacy"), ClientWire: strp("")},
			wantServer: "legacy",
			wantClient: "stateless",
		},
		{
			name:       "client override beats primary (incl dual mapping)",
			wf:         WireFlags{Wire: strp("dual"), ServerWire: strp(""), ClientWire: strp("stateless")},
			wantServer: "dual",
			wantClient: "stateless",
		},
		{
			name:       "nil pointers resolve to empty",
			wf:         WireFlags{},
			wantServer: "",
			wantClient: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.wf.serverToken(); got != tc.wantServer {
				t.Errorf("serverToken() = %q, want %q", got, tc.wantServer)
			}
			if got := tc.wf.clientToken(); got != tc.wantClient {
				t.Errorf("clientToken() = %q, want %q", got, tc.wantClient)
			}
		})
	}
}

func TestWireFlags_Options(t *testing.T) {
	// Selected wire yields an option.
	wf := WireFlags{Wire: strp("stateless")}
	if opt, ok := wf.ServerTransportOption(); !ok || opt == nil {
		t.Errorf("ServerTransportOption() for stateless = (%v,%v), want non-nil,true", opt, ok)
	}
	if opt, ok := wf.ClientOption(); !ok || opt == nil {
		t.Errorf("ClientOption() for stateless = (%v,%v), want non-nil,true", opt, ok)
	}

	// Empty wire selects nothing (fall through to env/default).
	empty := WireFlags{}
	if opt, ok := empty.ServerTransportOption(); ok || opt != nil {
		t.Errorf("ServerTransportOption() for empty = (%v,%v), want nil,false", opt, ok)
	}
	if opt, ok := empty.ClientOption(); ok || opt != nil {
		t.Errorf("ClientOption() for empty = (%v,%v), want nil,false", opt, ok)
	}

	// Unrecognized token warns and falls through.
	bad := WireFlags{Wire: strp("bogus")}
	if _, ok := bad.ServerTransportOption(); ok {
		t.Errorf("ServerTransportOption() for bogus = ok=true, want false")
	}
	if _, ok := bad.ClientOption(); ok {
		t.Errorf("ClientOption() for bogus = ok=true, want false")
	}
}

func TestWireFromArgs(t *testing.T) {
	// WireFromArgs reads os.Args; this just verifies the parser shape on
	// a synthetic slice would resolve. Since WireFromArgs scans os.Args
	// directly, cover the resolution via a constructed WireFlags instead
	// (the scan logic mirrors ExporterFromArgs, exercised by its own
	// test). Here we assert the default (no relevant args) is empty.
	wf := WireFromArgs()
	if got := wf.serverToken(); got != "" {
		t.Errorf("WireFromArgs with no wire args: serverToken() = %q, want empty", got)
	}
	if got := wf.clientToken(); got != "" {
		t.Errorf("WireFromArgs with no wire args: clientToken() = %q, want empty", got)
	}
}
