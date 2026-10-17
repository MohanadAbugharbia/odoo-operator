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

// Package odooconf reads the database connection from the odoo.conf the
// operator renders, so the backup Jobs connect exactly as Odoo does.
package odooconf

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Config is the part of odoo.conf the backup Jobs need.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
	DataDir  string
}

// Load reads odoo.conf from path.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads the [options] section of odoo.conf.
func Parse(r io.Reader) (Config, error) {
	cfg := Config{Port: 5432, SSLMode: "prefer"}
	section := ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		if section != "options" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == "False" {
			value = ""
		}
		switch key {
		case "db_host":
			cfg.Host = value
		case "db_port":
			if value == "" {
				continue
			}
			port, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("odoo.conf: db_port %q is not a number", value)
			}
			cfg.Port = port
		case "db_user":
			cfg.User = value
		case "db_password":
			cfg.Password = value
		case "db_name":
			cfg.Name = value
		case "db_sslmode":
			if value != "" {
				cfg.SSLMode = value
			}
		case "data_dir":
			cfg.DataDir = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	if cfg.Name == "" {
		return Config{}, fmt.Errorf("odoo.conf has no db_name")
	}
	if strings.Contains(cfg.Name, ",") {
		return Config{}, fmt.Errorf("odoo.conf db_name %q names several databases", cfg.Name)
	}
	return cfg, nil
}

// Env returns the libpq environment for the connection, for pg_dump and psql.
func (c Config) Env(database string) []string {
	env := []string{
		"PGPORT=" + strconv.Itoa(c.Port),
		"PGDATABASE=" + database,
		"PGSSLMODE=" + c.SSLMode,
		"PGAPPNAME=odoo-operator-backup",
	}
	if c.Host != "" {
		env = append(env, "PGHOST="+c.Host)
	}
	if c.User != "" {
		env = append(env, "PGUSER="+c.User)
	}
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
	}
	return env
}
