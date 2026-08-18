package mc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Server-jar discovery against the official distribution APIs, so the panel
// can offer a version picker instead of asking for a URL.
//
// The HTTP client is supplied by the caller (the web layer passes its
// SSRF-hardened client), and every response is size-limited.

const apiReadLimit = 8 << 20

type ServerFlavor string

const (
	FlavorVanilla ServerFlavor = "vanilla"
	FlavorPaper   ServerFlavor = "paper"
	FlavorPurpur  ServerFlavor = "purpur"
	FlavorFabric  ServerFlavor = "fabric"
)

// Flavors lists what the installer supports, for the UI.
var Flavors = []ServerFlavor{FlavorPaper, FlavorPurpur, FlavorVanilla, FlavorFabric}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "BlockPanel")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, apiReadLimit))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// ListVersions returns the available Minecraft versions for a flavor,
// newest first.
func ListVersions(ctx context.Context, client *http.Client, flavor ServerFlavor) ([]string, error) {
	switch flavor {
	case FlavorPaper:
		// Paper's v3 ("fill") API groups versions by family, newest family
		// first, with each family's versions newest-first inside it.
		var out struct {
			Versions map[string][]string `json:"versions"`
		}
		if err := getJSON(ctx, client, "https://fill.papermc.io/v3/projects/paper", &out); err != nil {
			return nil, err
		}
		families := make([]string, 0, len(out.Versions))
		for f := range out.Versions {
			families = append(families, f)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(families)))
		var ids []string
		for _, f := range families {
			ids = append(ids, out.Versions[f]...)
		}
		return ids, nil

	case FlavorPurpur:
		var out struct {
			Versions []string `json:"versions"`
		}
		if err := getJSON(ctx, client, "https://api.purpurmc.org/v2/purpur", &out); err != nil {
			return nil, err
		}
		return reverse(out.Versions), nil

	case FlavorVanilla:
		var out struct {
			Versions []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"versions"`
		}
		if err := getJSON(ctx, client, "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json", &out); err != nil {
			return nil, err
		}
		var ids []string
		for _, v := range out.Versions {
			if v.Type == "release" {
				ids = append(ids, v.ID)
			}
		}
		return ids, nil // manifest is already newest-first

	case FlavorFabric:
		var out []struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if err := getJSON(ctx, client, "https://meta.fabricmc.net/v2/versions/game", &out); err != nil {
			return nil, err
		}
		var ids []string
		for _, v := range out {
			if v.Stable {
				ids = append(ids, v.Version)
			}
		}
		return ids, nil
	}
	return nil, fmt.Errorf("unknown server type %q", flavor)
}

// ResolveDownload returns the direct jar URL and a suggested filename for a
// flavor+version, picking the newest build where the flavor has builds.
func ResolveDownload(ctx context.Context, client *http.Client, flavor ServerFlavor, version string) (url, filename string, err error) {
	if version == "" || strings.ContainsAny(version, "/?&#") {
		return "", "", fmt.Errorf("invalid version")
	}
	switch flavor {
	case FlavorPaper:
		// v3 hands back the newest build with a direct download URL.
		var build struct {
			ID        int `json:"id"`
			Downloads map[string]struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"downloads"`
		}
		u := fmt.Sprintf("https://fill.papermc.io/v3/projects/paper/versions/%s/builds/latest", version)
		if err := getJSON(ctx, client, u, &build); err != nil {
			return "", "", err
		}
		dl, ok := build.Downloads["server:default"]
		if !ok {
			for _, d := range build.Downloads { // fall back to whatever is offered
				dl = d
				ok = true
				break
			}
		}
		if !ok || dl.URL == "" {
			return "", "", fmt.Errorf("no Paper build available for %s", version)
		}
		name := dl.Name
		if name == "" {
			name = fmt.Sprintf("paper-%s-%d.jar", version, build.ID)
		}
		return dl.URL, name, nil

	case FlavorPurpur:
		var builds struct {
			Builds struct {
				Latest string `json:"latest"`
			} `json:"builds"`
		}
		u := fmt.Sprintf("https://api.purpurmc.org/v2/purpur/%s", version)
		if err := getJSON(ctx, client, u, &builds); err != nil {
			return "", "", err
		}
		if builds.Builds.Latest == "" {
			return "", "", fmt.Errorf("no Purpur builds for %s", version)
		}
		return fmt.Sprintf("https://api.purpurmc.org/v2/purpur/%s/%s/download", version, builds.Builds.Latest),
			fmt.Sprintf("purpur-%s-%s.jar", version, builds.Builds.Latest), nil

	case FlavorVanilla:
		var manifest struct {
			Versions []struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"versions"`
		}
		if err := getJSON(ctx, client, "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json", &manifest); err != nil {
			return "", "", err
		}
		var metaURL string
		for _, v := range manifest.Versions {
			if v.ID == version {
				metaURL = v.URL
				break
			}
		}
		if metaURL == "" {
			return "", "", fmt.Errorf("unknown Minecraft version %s", version)
		}
		var meta struct {
			Downloads struct {
				Server struct {
					URL string `json:"url"`
				} `json:"server"`
			} `json:"downloads"`
		}
		if err := getJSON(ctx, client, metaURL, &meta); err != nil {
			return "", "", err
		}
		if meta.Downloads.Server.URL == "" {
			return "", "", fmt.Errorf("version %s has no server download", version)
		}
		return meta.Downloads.Server.URL, fmt.Sprintf("minecraft_server-%s.jar", version), nil

	case FlavorFabric:
		// Fabric publishes a ready-to-run launcher jar per (game, loader,
		// installer) triple; take the newest stable loader and installer.
		var loaders []struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if err := getJSON(ctx, client, "https://meta.fabricmc.net/v2/versions/loader", &loaders); err != nil {
			return "", "", err
		}
		var installers []struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if err := getJSON(ctx, client, "https://meta.fabricmc.net/v2/versions/installer", &installers); err != nil {
			return "", "", err
		}
		loader, installer := "", ""
		for _, l := range loaders {
			if l.Stable {
				loader = l.Version
				break
			}
		}
		for _, i := range installers {
			if i.Stable {
				installer = i.Version
				break
			}
		}
		if loader == "" || installer == "" {
			return "", "", fmt.Errorf("no stable Fabric loader/installer available")
		}
		return fmt.Sprintf("https://meta.fabricmc.net/v2/versions/loader/%s/%s/%s/server/jar",
				version, loader, installer),
			fmt.Sprintf("fabric-server-%s-%s.jar", version, loader), nil
	}
	return "", "", fmt.Errorf("unknown server type %q", flavor)
}

// reverse returns in with the order flipped; the Purpur API lists versions
// oldest-first and the UI wants newest-first.
func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
