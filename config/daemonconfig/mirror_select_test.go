/*
 * Copyright (c) 2026. Nydus Developers. All rights reserved.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package daemonconfig

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var testRegistryHost = "fake-test.registry.com"

func writeMirrorHostsToml(t *testing.T, dir, content string) {
	t.Helper()
	hostDir := filepath.Join(dir, testRegistryHost)
	require.NoError(t, os.MkdirAll(hostDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "hosts.toml"), []byte(content), 0600))
}

func TestSplitMirrorURL(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		expectedScheme string
		expectedHost   string
		expectErr      bool
	}{
		{
			name:           "http with port",
			input:          "http://mirror:5000",
			expectedScheme: "http",
			expectedHost:   "mirror:5000",
		},
		{
			name:           "https without port",
			input:          "https://mirror.example.com",
			expectedScheme: "https",
			expectedHost:   "mirror.example.com",
		},
		{
			name:           "no scheme, host only",
			input:          "mirror.example.com",
			expectedScheme: "https",
			expectedHost:   "mirror.example.com",
		},
		{
			name:           "no scheme, host with port",
			input:          "mirror:5000",
			expectedScheme: "https",
			expectedHost:   "mirror:5000",
		},
		{
			name:           "https with port",
			input:          "https://mirror.example.com:5000",
			expectedScheme: "https",
			expectedHost:   "mirror.example.com:5000",
		},
		{
			name:           "http with path",
			input:          "http://mirror.example.com/v2",
			expectedScheme: "http",
			expectedHost:   "mirror.example.com/v2",
		},
		{
			name:           "https with proxy path prefix",
			input:          "https://proxy.example.test/docker.io",
			expectedScheme: "https",
			expectedHost:   "proxy.example.test/docker.io",
		},
		{
			name:           "https with proxy path prefix and trailing slash",
			input:          "https://proxy.example.test/docker.io/",
			expectedScheme: "https",
			expectedHost:   "proxy.example.test/docker.io",
		},
		{
			name:           "host with port and no path is unchanged",
			input:          "proxy.example.test:5000",
			expectedScheme: "https",
			expectedHost:   "proxy.example.test:5000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme, host, err := splitMirrorURL(tc.input)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedScheme, scheme, "scheme for %s", tc.input)
			require.Equal(t, tc.expectedHost, host, "host for %s", tc.input)
		})
	}
}

func TestSelectMirrorHost_NoConfig(t *testing.T) {
	scheme, host, _ := selectMirrorHost("", testRegistryHost)
	require.Equal(t, testRegistryHost, host)
	require.Equal(t, "", scheme)
}

func TestSelectMirrorHost_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, testRegistryHost, host)
	require.Equal(t, "", scheme)
}

func TestSelectMirrorHost_MirrorNoPingURL(t *testing.T) {
	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."http://mirror1:5000"]
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, "mirror1:5000", host)
	require.Equal(t, "http", scheme)
}

func TestSelectMirrorHost_MirrorPingSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."http://mirror1:5000"]
    ping_url = "`+srv.URL+`"
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, "mirror1:5000", host)
	require.Equal(t, "http", scheme)
}

func TestSelectMirrorHost_MirrorPingFails_FallbackToOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."http://mirror1:5000"]
    ping_url = "`+srv.URL+`"
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, testRegistryHost, host)
	require.Equal(t, "", scheme)
}

func TestSelectMirrorHost_FirstMirrorFails_SecondMirrorNoPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."http://mirror1:5000"]
    ping_url = "`+srv.URL+`"
  [host."https://mirror2.example.com"]
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, "mirror2.example.com", host)
	require.Equal(t, "https", scheme)
}

// A mirror that serves the upstream registry under a path prefix must keep that
// prefix, otherwise nydusd resolves blobs against the mirror root and 404s.
func TestSelectMirrorHost_MirrorPathPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."https://proxy.example.test/docker.io"]
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, "https", scheme)
	require.Equal(t, "proxy.example.test/docker.io", host)
}

// The host written into the generated nydusd configuration (what nydusd reads for
// [remote] / backend host resolution) must carry the mirror path prefix.
func TestFuseDaemonConfig_MirrorPathInGeneratedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	writeMirrorHostsToml(t, tmpDir, `
[host]
  [host."https://proxy.example.test/docker.io"]
`)
	scheme, host, _ := selectMirrorHost(tmpDir, testRegistryHost)
	require.Equal(t, "proxy.example.test/docker.io", host)

	cfg := &FuseDaemonConfig{Device: &DeviceConfig{}}
	cfg.Device.Backend.BackendType = backendTypeRegistry
	cfg.Supplement(host, "library/python", "snap-1", map[string]string{CacheDir: "/var/lib/nydus/cache"})
	cfg.Device.Backend.Config.Scheme = scheme

	dump, err := cfg.DumpString()
	require.NoError(t, err)
	require.Contains(t, dump, `"host":"proxy.example.test/docker.io"`)
	require.Contains(t, dump, `"repo":"library/python"`)
	require.Contains(t, dump, `"scheme":"https"`)
}
