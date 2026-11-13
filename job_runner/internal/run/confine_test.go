package run

import (
	"context"
	"testing"

	"github.com/jurasikasan/units/job_runner/internal/engine/enginetest"

	"github.com/stretchr/testify/require"
)

// The expected values below come from CONTRACT.md, sections 2, 3 and 5.

// Inside a run: gVisor's kernel, no capabilities, no_new_privs, the limits
// of the server, and no network unless one is named.
func TestARunIsConfined(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	_, err := r.Run(context.Background(), Request{Key: "k", Script: "true"})
	require.NoError(t, err)

	h := runs(f)[0].Host
	require.Equal(t, "runsc", h["Runtime"])
	require.Equal(t, []any{"ALL"}, h["CapDrop"])
	require.Equal(t, []any{"no-new-privileges"}, h["SecurityOpt"])
	require.Equal(t, float64(256), h["PidsLimit"])
	require.Equal(t, float64(1<<30), h["Memory"])
	require.Equal(t, float64(1<<30), h["MemorySwap"], "no swap past the memory limit")
	require.Equal(t, float64(2e9), h["NanoCpus"])
	require.Equal(t, "none", h["NetworkMode"], "none is the default")
	require.Equal(t, "1000:1000", runs(f)[0].User)

	// The folder's preparation runs under gVisor, without a network, too.
	var prep enginetest.Created
	for _, c := range f.Log {
		if c.Labels[labelKey] == "" {
			prep = c
		}
	}
	require.Equal(t, "runsc", prep.Host["Runtime"])
	require.Equal(t, "none", prep.Host["NetworkMode"])
}

// network: none, internet (the daemon's filtered bridge), or a deployer's
// name; anything else is refused before anything is made.
func TestNetworks(t *testing.T) {
	f := enginetest.New("bash:5.2")
	r := newRunner(t, f)
	r.Networks = map[string]string{"gateway": "jr_gateway"}
	for i, c := range []struct{ name, mode string }{
		{"none", "none"}, {"internet", "bridge"}, {"gateway", "jr_gateway"},
	} {
		_, err := r.Run(context.Background(), Request{Key: "n" + c.name, Script: "true", Network: c.name})
		require.NoError(t, err)
		require.Equal(t, c.mode, runs(f)[i].Host["NetworkMode"], c.name)
		require.Equal(t, c.name, runs(f)[i].Labels[labelNetwork])
	}
	before := len(f.Log)
	for _, n := range []string{"bridge", "host", "jr_gateway", "lan"} {
		_, err := r.Run(context.Background(), Request{Key: "x", Script: "true", Network: n})
		var ue *UsageError
		require.ErrorAs(t, err, &ue, n)
	}
	require.Len(t, f.Log, before)
}
