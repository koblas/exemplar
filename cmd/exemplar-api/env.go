package main

import (
	"os"
	"strconv"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/setup"
)

// env is where exemplar-api reads its settings and secret files: the
// process's own, or those with what setup just wrote laid over them.
type env struct {
	get  func(string) string
	read func(string) ([]byte, error)
}

func osEnv() env {
	return env{get: os.Getenv, read: os.ReadFile}
}

// with is e as a pod started after setup would see it: the App's id and
// client id, and the database URL if e had none, as variables, and the
// private key, client secret and root key at the paths e names for them.
// The kubelet catches the mounted files up later; this pod doesn't wait.
func (e env) with(in *setup.Installed) env {
	vars := map[string]string{
		"EXEMPLAR_GH_APP_ID":               strconv.FormatInt(in.AppID, 10),
		"EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID": in.ClientID,
	}
	if e.get("EXEMPLAR_DATABASE_URL") == "" {
		vars["EXEMPLAR_DATABASE_URL"] = in.DatabaseURL
	}
	files := map[string][]byte{}
	for name, b := range map[string][]byte{
		"EXEMPLAR_GH_PRIVATE_KEY_FILE":              in.PrivateKey,
		"EXEMPLAR_OAUTH2_GITHUB_CLIENT_SECRET_FILE": in.ClientSecret,
		envcfg.RootKeyFile:                          in.RootKey,
	} {
		if p := e.get(name); p != "" {
			files[p] = b
		}
	}
	return env{
		get: func(k string) string {
			if v, ok := vars[k]; ok {
				return v
			}
			return e.get(k)
		},
		read: func(p string) ([]byte, error) {
			if b, ok := files[p]; ok {
				return b, nil
			}
			return e.read(p)
		},
	}
}
