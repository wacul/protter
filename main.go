package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/kingpin"
	"github.com/kyoh86/fastwalk"
	"golang.org/x/net/publicsuffix"
)

type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func main() {
	app := kingpin.New("protter", "upload exported sketch artboards to prott")

	var flags struct {
		// CookieFile string
		CWD           string
		ProttEmail    string
		ProttPassword string
	}
	// app.Flag("cookie-file", "filepath to save / restore a login session").Default("cookie.jar").StringVar(&flags.CookieFile)
	app.Flag("current-directory", "Run as if git was started in <path> instead of the current working directory.").Default(".").Short('C').PlaceHolder("<path>").ExistingDirVar(&flags.CWD)
	app.Flag("prott-email", "an email of the account of the Prott.app").Envar("PROTT_EMAIL").StringVar(&flags.ProttEmail)
	app.Flag("prott-password", "a password of the account of the Prott.app").Envar("PROTT_PASSWORD").StringVar(&flags.ProttPassword)

	if _, err := app.Parse(os.Args[1:]); err != nil {
		panic(err)
	}

	client, err := buildClient()
	if err != nil {
		panic(err)
	}

	// login
	if err := loginPrott(client, flags.ProttEmail, flags.ProttPassword); err != nil {
		panic(err)
	}

	// get projects list
	projectList, err := getProjectList(client)
	if err != nil {
		panic(err)
	}
	projects := map[string]Project{}
	for _, p := range projectList {
		projects[p.Name] = p
		fmt.Println(p.Name)
	}

	if err := fastwalk.FastWalk(flags.CWD, func(path string, typ os.FileMode) error {
		projectName, screenName, err := parsePath(path)
		switch err {
		case nil:
			// noop
		case errInvalidPath:
			return nil
		default:
			return err
		}
		project, ok := projects[projectName]
		if !ok {
			fmt.Printf("a project %q is not exist\n", projectName)
			return nil // skip
		}

		return uploadScreen(client, project, screenName, path)

	}); err != nil {
		panic(err)
	}
}

func buildClient() (*http.Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: publicsuffix.List,
	})
	if err != nil {
		return nil, err
	}
	// TODO: setCookies from cookie-file
	client := &http.Client{
		Jar: jar,
	}
	return client, nil
}

func loginPrott(client *http.Client, email, pass string) error {
	token := map[string]interface{}{
		"user": map[string]interface{}{
			"email":    email,
			"password": pass,
		},
	}
	js, err := json.Marshal(token)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", "https://prottapp.com/users/sign_in.json", bytes.NewBuffer(js))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sketch")
	req.Header.Set("App-Type", "sketch")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return errors.New("invalid login")
	}
	return nil
}

func getProjectList(client *http.Client) ([]Project, error) {
	type account struct {
		Name     string
		Projects []Project
	}
	var accountMap map[string]account
	req, err := http.NewRequest("GET", "https://prottapp.com/api/sketch_app/projects.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sketch")
	req.Header.Set("App-Type", "sketch")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("failed to get projects")
	}
	if res.Body == nil {
		return nil, errors.New("failed to get projects")
	}
	defer res.Body.Close()
	decoder := json.NewDecoder(res.Body)
	if err := decoder.Decode(&accountMap); err != nil {
		return nil, err
	}
	var projects []Project
	for _, p := range accountMap {
		projects = append(projects, p.Projects...)
	}
	return projects, nil
}

var (
	screenReg      *regexp.Regexp
	errInvalidPath = errors.New("invalid path")
)

func init() {
	screenReg = regexp.MustCompile(
		`(?:^|` +
			string([]rune{filepath.Separator}) +
			`).exportedArtboards` +
			string([]rune{filepath.Separator}) +
			`(.*\.png)$`)
}

func parsePath(path string) (string, string, error) {
	mat := screenReg.FindStringSubmatch(path)
	if len(mat) <= 1 {
		return "", "", errInvalidPath
	}
	return filepath.Dir(mat[1]), strings.TrimSuffix(filepath.Base(mat[1]), `.png`), nil
}

func uploadScreen(client *http.Client, project Project, screen, path string) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if fw, err := w.CreateFormField("project_id"); err != nil {
		return err
	} else if _, err = fw.Write([]byte(project.ID)); err != nil {
		return err
	}
	if fw, err := w.CreateFormField("screen[sketch_artboard_id]"); err != nil {
		return err
	} else if _, err = fw.Write([]byte(screen)); err != nil {
		// TODO: get sketch artboard id instead of its name
		return err
	}
	if fw, err := w.CreateFormField("screen[name]"); err != nil {
		return err
	} else if _, err = fw.Write([]byte(screen)); err != nil {
		return err
	}
	// Add your image file
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if fw, err := w.CreateFormFile("screen[file]", path); err != nil {
		return err
	} else if _, err = io.Copy(fw, f); err != nil {
		return err
	}
	w.Close()

	req, err := http.NewRequest("POST", "https://prottapp.com/api/sketch_app/screens.json", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", "sketch")
	req.Header.Set("App-Type", "sketch")
	if res, err := client.Do(req); err != nil {
		return err
	} else {
		fmt.Println(res.Status)
	}
	fmt.Println(project.Name, screen)
	return nil
}
