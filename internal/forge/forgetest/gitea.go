package forgetest

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

func (f *Fake) gtRepo(r *Repo, login string) map[string]any {
	full := r.Owner + "/" + r.Name
	l := f.levelLocked(login, r)
	return map[string]any{"id": r.ID, "name": r.Name, "full_name": full, "html_url": f.URL + "/" + full, "clone_url": f.URL + "/" + full + ".git",
		"private": r.Private, "default_branch": "main", "description": r.Desc, "owner": map[string]any{"login": r.Owner, "username": r.Owner},
		"permissions": gtPerms{Admin: l >= Owner, Push: l >= Developer, Pull: l >= Guest}}
}

// gtPerms is a repository's permissions in Gitea's answer.
type gtPerms struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

func gtError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "url": "/api/swagger"})
}

func (f *Fake) serveGitea(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/version" {
		if f.NotThisPlatform {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, map[string]string{"version": f.Version})
		return
	}
	me, ok := f.who(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	seg := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/user" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"id": me.ID, "login": me.Login, "username": me.Login, "is_admin": f.admins[strings.ToLower(me.Login)]})
	case path == "/user/teams" && r.Method == http.MethodGet:
		var gs []*Group
		for _, g := range f.groups {
			if g.Members[me.Login] > 0 {
				gs = append(gs, g)
			}
		}
		sort.Slice(gs, func(i, j int) bool { return gs[i].Path < gs[j].Path })
		var teams []map[string]any
		for _, g := range gs {
			l := g.Members[me.Login]
			perm := "read"
			switch {
			case l >= Owner:
				perm = "owner"
			case l >= Maintainer:
				perm = "admin"
			case l >= Developer:
				perm = "write"
			}
			org := map[string]any{"id": g.ID, "username": g.Path, "name": g.Path}
			teams = append(teams, map[string]any{"id": g.ID*10 + 1, "name": "Team", "permission": perm, "can_create_org_repo": l >= g.CreateLevel, "organization": org})
			// A second team of the same organisation: an organisation is listed once.
			teams = append(teams, map[string]any{"id": g.ID*10 + 2, "name": "Everyone", "permission": "read", "can_create_org_repo": false, "organization": org})
		}
		lo, hi, next := page(r, len(teams))
		setNext(w, r, "gitea", next)
		writeJSON(w, 200, teams[lo:hi])
	case len(seg) == 5 && seg[0] == "users" && seg[2] == "orgs" && seg[4] == "permissions" && r.Method == http.MethodGet:
		who, _ := url.PathUnescape(seg[1])
		org, _ := url.PathUnescape(seg[3])
		g := f.groups[strings.ToLower(org)]
		if !strings.EqualFold(who, me.Login) || g == nil || g.Members[me.Login] == 0 {
			gtError(w, 404, "GetOrgByName: org does not exist")
			return
		}
		l := g.Members[me.Login]
		writeJSON(w, 200, map[string]any{"can_create_repository": l >= g.CreateLevel, "can_read": true, "can_write": l >= Developer,
			"is_admin": l >= Maintainer, "is_owner": l >= Owner})
	case path == "/user/repos" && r.Method == http.MethodPost:
		f.gtCreate(w, r, me, me.Login)
	case len(seg) == 3 && seg[0] == "orgs" && seg[2] == "repos" && r.Method == http.MethodPost:
		org, _ := url.PathUnescape(seg[1])
		g := f.groups[strings.ToLower(org)]
		if g == nil {
			gtError(w, 404, "GetOrgByName: org does not exist")
			return
		}
		if l := g.Members[me.Login]; l == 0 || l < g.CreateLevel {
			gtError(w, 403, "Given user is not allowed to create repository in organization.")
			return
		}
		f.gtCreate(w, r, me, g.Path)
	case path == "/user/repos" && r.Method == http.MethodGet:
		var list []*Repo
		for _, repo := range f.repos {
			if f.memberLevelLocked(me.Login, repo) > 0 {
				list = append(list, repo)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
		lo, hi, next := page(r, len(list))
		out := []map[string]any{}
		for _, repo := range list[lo:hi] {
			out = append(out, f.gtRepo(repo, me.Login))
		}
		setNext(w, r, "gitea", next)
		writeJSON(w, 200, out)
	case path == "/repos/search" && r.Method == http.MethodGet:
		search := strings.ToLower(q.Get("q"))
		var list []*Repo
		for _, repo := range f.repos {
			if f.levelLocked(me.Login, repo) > 0 && strings.Contains(strings.ToLower(repo.Name), search) {
				list = append(list, repo)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
		lo, hi, next := page(r, len(list))
		out := []map[string]any{}
		for _, repo := range list[lo:hi] {
			out = append(out, f.gtRepo(repo, me.Login))
		}
		setNext(w, r, "gitea", next)
		writeJSON(w, 200, map[string]any{"ok": true, "data": out})
	case len(seg) == 3 && seg[0] == "repos" && r.Method == http.MethodGet:
		o, _ := url.PathUnescape(seg[1])
		n, _ := url.PathUnescape(seg[2])
		repo := f.repos[strings.ToLower(o+"/"+n)]
		if repo == nil || f.levelLocked(me.Login, repo) == 0 {
			gtError(w, 404, "The target couldn't be found.")
			return
		}
		writeJSON(w, 200, f.gtRepo(repo, me.Login))
	default:
		gtError(w, 404, "not found")
	}
}

func (f *Fake) gtCreate(w http.ResponseWriter, r *http.Request, me *User, owner string) {
	var body struct {
		Name        string `json:"name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
	}
	readJSON(r, &body)
	if _, taken := f.repos[strings.ToLower(owner+"/"+body.Name)]; taken {
		gtError(w, 409, "The repository with the same name already exists.")
		return
	}
	repo := f.newRepoLocked(owner, body.Name, body.Private, body.Description)
	writeJSON(w, 201, f.gtRepo(repo, me.Login))
}
