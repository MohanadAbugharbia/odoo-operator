/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

// Environment of the store remote the Jobs configure for rclone.
const (
	envKnownHosts        = "RCLONE_CONFIG_STORE_KNOWN_HOSTS_FILE"
	envHostKeyAlgorithms = "RCLONE_CONFIG_STORE_HOST_KEY_ALGORITHMS"
)

// hostKeyAlgorithms maps a known_hosts key type to the algorithms that
// verify it, as the SSH client offers them.
var hostKeyAlgorithms = map[string][]string{
	"ssh-ed25519":         {"ssh-ed25519"},
	"ecdsa-sha2-nistp256": {"ecdsa-sha2-nistp256"},
	"ecdsa-sha2-nistp384": {"ecdsa-sha2-nistp384"},
	"ecdsa-sha2-nistp521": {"ecdsa-sha2-nistp521"},
	"ssh-rsa":             {"rsa-sha2-512", "rsa-sha2-256", "ssh-rsa"},
}

// LimitHostKeyAlgorithms makes rclone ask an SFTP server only for the host
// key types known_hosts holds. Without it the server may present another
// of its keys (an RSA one, when known_hosts has its ed25519 key) and the
// connection fails with "knownhosts: key mismatch". It does this for every
// remote with a known_hosts file (the store and each copy store), and
// nothing for a remote whose algorithms are already set.
func LimitHostKeyAlgorithms() error {
	for _, kv := range os.Environ() {
		key, path, _ := strings.Cut(kv, "=")
		remote, ok := strings.CutSuffix(key, "_KNOWN_HOSTS_FILE")
		if !ok || !strings.HasPrefix(remote, "RCLONE_CONFIG_") || path == "" {
			continue
		}
		target := remote + "_HOST_KEY_ALGORITHMS"
		if os.Getenv(target) != "" {
			continue
		}
		algorithms, err := knownHostAlgorithms(path)
		if err != nil {
			return err
		}
		if len(algorithms) == 0 {
			continue
		}
		if err := os.Setenv(target, strings.Join(algorithms, " ")); err != nil {
			return err
		}
	}
	return nil
}

// knownHostAlgorithms lists the algorithms for the key types in a
// known_hosts file, in the order they first appear.
func knownHostAlgorithms(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	seen := map[string]bool{}
	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if strings.HasPrefix(fields[0], "@") { // @cert-authority, @revoked
			fields = fields[1:]
		}
		if len(fields) < 3 {
			continue
		}
		for _, a := range hostKeyAlgorithms[fields[1]] {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out, scanner.Err()
}
