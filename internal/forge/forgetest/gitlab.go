package forgetest

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// memberLevelLocked is the level a person has through membership only (a
// public repository's Guest level does not make anyone a member).
func (f *Fake) memberLevelLocked(login string, repo *Repo) int {
	if strings.EqualFold(repo.Owner, login) {
		return Owner
	}
	best := repo.Collab[login]
	if g := f.groups[strings.ToLower(repo.Owner)]; g != nil {
		if l := g.Members[login]; l > best {
			best = l
		}
	}
	return best
}

func glJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

func (f *Fake) glProject(r *Repo, login string) map[string]any {
	full := r.Owner + "/" + r.Name
	vis := "private"
	if !r.Private {
		vis = "public"
	}
	perms := map[string]any{"project_access": nil, "group_access": nil}
	level := f.memberLevelLocked(login, r)
	if g := f.groups[strings.ToLower(r.Owner)]; g != nil {
		if l := g.Members[login]; l > 0 {
			perms["group_access"] = map[string]any{"access_level": l}
		}
		if l := r.Collab[login]; l > 0 {
			perms["project_access"] = map[string]any{"access_level": l}
		}
	} else if level > 0 {
		perms["project_access"] = map[string]any{"access_level": level}
	}
	return map[string]any{"id": r.ID, "name": r.Name, "path": r.Name, "path_with_namespace": full, "visibility": vis,
		"default_branch": "main", "web_url": f.URL + "/" + full, "http_url_to_repo": f.URL + "/" + full + ".git",
		"description": r.Desc, "namespace": map[string]any{"full_path": r.Owner}, "permissions": perms}
}

func (f *Fake) serveGitLab(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/version" {
		if f.NotThisPlatform {
			http.NotFound(w, r)
			return
		}
		if _, ok := f.who(w, r); !ok {
			return
		}
		writeJSON(w, 200, map[string]any{"version": f.Version, "revision": "0123456789a"})
		return
	}
	me, ok := f.who(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	switch {
	case path == "/user" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"id": me.ID, "username": me.Login, "name": me.Login})
	case path == "/groups" && r.Method == http.MethodGet:
		min, _ := strconv.Atoi(q.Get("min_access_level"))
		search := strings.ToLower(q.Get("search"))
		var list []*Group
		for _, g := range f.groups {
			l := g.Members[me.Login]
			if l == 0 || l < min {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(g.Path), search) {
				continue
			}
			list = append(list, g)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
		lo, hi, next := page(r, len(list))
		out := []map[string]any{}
		for _, g := range list[lo:hi] {
			out = append(out, map[string]any{"id": g.ID, "full_path": g.Path, "name": g.Path[strings.LastIndex(g.Path, "/")+1:], "path": g.Path})
		}
		setNext(w, r, "gitlab", next)
		writeJSON(w, 200, out)
	case path == "/projects" && r.Method == http.MethodPost:
		f.glCreate(w, r, me)
	case path == "/projects" && r.Method == http.MethodGet:
		min, _ := strconv.Atoi(q.Get("min_access_level"))
		search := strings.ToLower(q.Get("search"))
		var list []*Repo
		for _, repo := range f.repos {
			l := f.memberLevelLocked(me.Login, repo)
			if l == 0 || l < min || (search != "" && !strings.Contains(strings.ToLower(repo.Name), search)) {
				continue
			}
			list = append(list, repo)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
		lo, hi, next := page(r, len(list))
		out := []map[string]any{}
		for _, repo := range list[lo:hi] {
			out = append(out, f.glProject(repo, me.Login))
		}
		setNext(w, r, "gitlab", next)
		writeJSON(w, 200, out)
	case strings.HasPrefix(path, "/projects/") && r.Method == http.MethodGet:
		key := strings.TrimPrefix(path, "/projects/")
		var repo *Repo
		if n, err := strconv.ParseInt(key, 10, 64); err == nil {
			for _, x := range f.repos {
				if x.ID == n {
					repo = x
				}
			}
		} else if p, err := url.PathUnescape(key); err == nil {
			repo = f.repos[strings.ToLower(p)]
		}
		if repo == nil || f.levelLocked(me.Login, repo) == 0 {
			glJSONError(w, 404, "404 Project Not Found")
			return
		}
		writeJSON(w, 200, f.glProject(repo, me.Login))
	default:
		glJSONError(w, 404, "404 Not Found")
	}
}

func (f *Fake) glCreate(w http.ResponseWriter, r *http.Request, me *User) {
	var body struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Visibility  string `json:"visibility"`
		Description string `json:"description"`
		NamespaceID int64  `json:"namespace_id"`
	}
	readJSON(r, &body)
	owner := me.Login
	if body.NamespaceID != 0 {
		var g *Group
		for _, x := range f.groups {
			if x.ID == body.NamespaceID {
				g = x
			}
		}
		if g == nil {
			glJSONError(w, 404, "404 Namespace Not Found")
			return
		}
		if l := g.Members[me.Login]; l == 0 || l < g.CreateLevel {
			glJSONError(w, 403, "403 Forbidden")
			return
		}
		owner = g.Path
	}
	if body.Path == "" {
		body.Path = body.Name
	}
	if _, taken := f.repos[strings.ToLower(owner+"/"+body.Path)]; taken {
		writeJSON(w, 400, map[string]any{"message": map[string]any{"name": []string{"has already been taken"}, "path": []string{"has already been taken"}, "limit_reached": []string{}}})
		return
	}
	repo := f.newRepoLocked(owner, body.Path, body.Visibility != "public", body.Description)
	writeJSON(w, 201, f.glProject(repo, me.Login))
}
