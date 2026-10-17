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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

// ProbeOptions describe a store probe.
type ProbeOptions struct {
	// Rclone is the rclone executable; "rclone" when empty.
	Rclone string
	// Target is the store's root as rclone names it: "store:bucket/path"
	// for a remote configured through RCLONE_CONFIG_STORE_*, or a local
	// folder for nfs and pvc stores.
	Target string
}

// Probe writes a small file to the store, reads it back and deletes it, so a
// store that cannot be written (wrong credentials, read-only mount, unknown
// host key) is reported before the first backup needs it. The file name
// starts with .odoo-operator-probe- and is the only thing it ever deletes.
func Probe(ctx context.Context, o ProbeOptions) result.Result {
	bin := o.Rclone
	if bin == "" {
		bin = "rclone"
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return result.Failed(result.StepProbe, err)
	}
	name := ".odoo-operator-probe-" + hex.EncodeToString(token)
	file := strings.TrimSuffix(o.Target, "/") + "/" + name
	content := []byte("odoo-operator store probe " + time.Now().UTC().Format(time.RFC3339) + "\n")

	run := func(stdin []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"--retries", "1", "--low-level-retries", "2"}, args...)...)
		cmd.Env = os.Environ()
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("rclone %s: %w: %s", args[0], err, tail(stderr.String()))
		}
		return stdout.Bytes(), nil
	}

	if _, err := run(content, "rcat", file); err != nil {
		return result.Failed(result.StepProbe, fmt.Errorf("cannot write to the store: %w", err))
	}
	got, readErr := run(nil, "cat", file)
	_, delErr := run(nil, "deletefile", file)
	switch {
	case readErr != nil:
		return result.Failed(result.StepProbe, fmt.Errorf("cannot read back from the store: %w", readErr))
	case !bytes.Equal(got, content):
		return result.Failed(result.StepProbe, fmt.Errorf("the store returned different content than was written"))
	case delErr != nil:
		return result.Failed(result.StepProbe, fmt.Errorf("cannot delete from the store: %w", delErr))
	}
	return result.Result{OK: true}
}
