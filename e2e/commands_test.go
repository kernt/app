package e2e

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/app/internal"
	"github.com/docker/app/internal/yaml"
	"gotest.tools/assert"
	is "gotest.tools/assert/cmp"
	"gotest.tools/fs"
	"gotest.tools/golden"
	"gotest.tools/icmd"
	"gotest.tools/skip"
)

const (
	singleFileApp = `version: 0.1.0
name: helloworld
description: "hello world app"
namespace: "foo"
---
version: '3.5'
services:
  hello-world:
    image: hello-world
---
# This section contains the default values for your application settings.`
)

func TestRenderTemplatesBinary(t *testing.T) {
	skip.If(t, !hasExperimental, "experimental mode needed for this test")
	appsPath := filepath.Join("testdata", "templates")
	apps, err := ioutil.ReadDir(appsPath)
	assert.NilError(t, err, "unable to get apps")
	for _, app := range apps {
		appPath := filepath.Join(appsPath, app.Name())
		if !checkRenderers(app.Name(), renderers) {
			t.Log("Required renderer not enabled")
			continue
		}
		t.Run(app.Name(), testRenderApp(appPath, "DOCKERAPP_RENDERERS="+app.Name()))
	}
}

func TestRenderBinary(t *testing.T) {
	appsPath := filepath.Join("testdata", "render")
	apps, err := ioutil.ReadDir(appsPath)
	assert.NilError(t, err, "unable to get apps")
	for _, app := range apps {
		appPath := filepath.Join(appsPath, app.Name())
		t.Run(app.Name(), testRenderApp(appPath))
	}
}

func testRenderApp(appPath string, env ...string) func(*testing.T) {
	return func(t *testing.T) {
		envs := map[string]string{}
		data, err := ioutil.ReadFile(filepath.Join(appPath, "env.yml"))
		assert.NilError(t, err)
		assert.NilError(t, yaml.Unmarshal(data, &envs))
		args := []string{"render", filepath.Join(appPath, "my.dockerapp"),
			"-f", filepath.Join(appPath, "settings-0.yml"),
		}
		for k, v := range envs {
			args = append(args, "-s", fmt.Sprintf("%s=%s", k, v))
		}
		result := icmd.RunCmd(icmd.Cmd{
			Command: append([]string{dockerApp}, args...),
			Env:     env,
		}).Assert(t, icmd.Success)
		assert.Assert(t, is.Equal(readFile(t, filepath.Join(appPath, "expected.txt")), result.Stdout()), "rendering missmatch")
	}
}

func TestInitBinary(t *testing.T) {
	composeData := `version: "3.2"
services:
  nginx:
    image: nginx:${NGINX_VERSION}
    command: nginx $NGINX_ARGS
`
	meta := `# Version of the application
version: 0.1.0
# Name of the application
name: app-test
# A short description of the application
description: my cool app
# Namespace to use when pushing to a registry. This is typically your Hub username.
#namespace: myHubUsername
# List of application maintainers with name and email for each
maintainers:
  - name: bob
    email: 
  - name: joe
    email: joe@joe.com
`
	envData := "# some comment\nNGINX_VERSION=latest"
	dir := fs.NewDir(t, "app_input",
		fs.WithFile(internal.ComposeFileName, composeData),
		fs.WithFile(".env", envData),
	)
	defer dir.Remove()

	testAppName := "app-test"
	dirName := internal.DirNameFromAppName(testAppName)
	defer os.RemoveAll(dirName)

	icmd.RunCommand(dockerApp, "init", testAppName,
		"-c", dir.Join(internal.ComposeFileName),
		"-d", "my cool app",
		"-m", "bob",
		"-m", "joe:joe@joe.com",
	).Assert(t, icmd.Success)
	manifest := fs.Expected(
		t,
		fs.WithMode(0755),
		fs.WithFile(internal.MetadataFileName, meta, fs.WithMode(0644)), // too many variables, cheating
		fs.WithFile(internal.ComposeFileName, composeData, fs.WithMode(0644)),
		fs.WithFile(internal.SettingsFileName, "NGINX_ARGS: FILL ME\nNGINX_VERSION: latest\n", fs.WithMode(0644)),
	)
	assert.Assert(t, fs.Equal(dirName, manifest))

	// validate metadata with JSON Schema
	icmd.RunCommand(dockerApp, "validate", testAppName).Assert(t, icmd.Success)

	// test single-file init
	icmd.RunCommand(dockerApp, "init", "tac",
		"-c", dir.Join(internal.ComposeFileName),
		"-d", "my cool app",
		"-m", "bob",
		"-m", "joe:joe@joe.com",
		"-s",
	).Assert(t, icmd.Success)
	defer os.Remove("tac.dockerapp")
	appData, err := ioutil.ReadFile("tac.dockerapp")
	assert.NilError(t, err)
	golden.Assert(t, string(appData), "init-singlefile.dockerapp")
	// Check various commands work on single-file app package
	icmd.RunCommand(dockerApp, "inspect", "tac").Assert(t, icmd.Success)
	icmd.RunCommand(dockerApp, "render", "tac").Assert(t, icmd.Success)
}

func TestDetectAppBinary(t *testing.T) {
	// cwd = e2e
	dir := fs.NewDir(t, "detect-app-binary",
		fs.WithDir("helm.dockerapp", fs.FromDir("testdata/helm.dockerapp")),
		fs.WithDir("render",
			fs.WithDir("app1.dockerapp", fs.FromDir("testdata/render/envvariables/my.dockerapp")),
			fs.WithDir("app2.dockerapp", fs.FromDir("testdata/render/envvariables/my.dockerapp")),
		),
	)
	defer dir.Remove()
	cwd, err := os.Getwd()
	assert.NilError(t, err)
	assert.NilError(t, os.Chdir(dir.Path()))
	defer func() { assert.NilError(t, os.Chdir(cwd)) }()
	icmd.RunCommand(dockerApp, "inspect").Assert(t, icmd.Success)
	assert.NilError(t, os.Chdir(dir.Join("helm.dockerapp")))
	icmd.RunCommand(dockerApp, "inspect").Assert(t, icmd.Success)
	icmd.RunCommand(dockerApp, "inspect", ".").Assert(t, icmd.Success)
	assert.NilError(t, os.Chdir(dir.Join("render")))
	result := icmd.RunCommand(dockerApp, "inspect")
	result.Assert(t, icmd.Expected{
		ExitCode: 1,
		Err:      "Error: multiple applications found in current directory, specify the application name on the command line",
	})
}

func TestPackBinary(t *testing.T) {
	skip.If(t, !hasExperimental, "experimental mode needed for this test")
	tempDir, err := ioutil.TempDir("", "dockerapp")
	assert.NilError(t, err)
	defer os.RemoveAll(tempDir)
	icmd.RunCommand(dockerApp, "pack", "testdata/helm", "-o", filepath.Join(tempDir, "test.dockerapp")).Assert(t, icmd.Success)
	// check that our commands run on the packed version
	icmd.RunCommand(dockerApp, "inspect", filepath.Join(tempDir, "test")).Assert(t, icmd.Expected{
		Out: "myapp",
	})
	icmd.RunCommand(dockerApp, "render", filepath.Join(tempDir, "test")).Assert(t, icmd.Expected{
		Out: "nginx",
	})
	cwd, err := os.Getwd()
	assert.NilError(t, err)
	assert.NilError(t, os.Chdir(tempDir))
	defer func() { assert.NilError(t, os.Chdir(cwd)) }()
	icmd.RunCommand(dockerApp, "helm", "test").Assert(t, icmd.Success)
	_, err = os.Stat("test.chart/Chart.yaml")
	assert.NilError(t, err)
	assert.NilError(t, os.Mkdir("output", 0755))
	icmd.RunCommand(dockerApp, "unpack", "test", "-o", "output").Assert(t, icmd.Success)
	_, err = os.Stat("output/test.dockerapp/docker-compose.yml")
	assert.NilError(t, err)
}

func TestHelmBinary(t *testing.T) {
	t.Run("default", testHelmBinary(""))
	t.Run("v1beta1", testHelmBinary("v1beta1"))
	t.Run("v1beta2", testHelmBinary("v1beta2"))
}

func testHelmBinary(version string) func(*testing.T) {
	return func(t *testing.T) {
		dir := fs.NewDir(t, "testHelmBinary", fs.FromDir("testdata"))
		defer dir.Remove()
		cmd := []string{dockerApp, "helm", "helm", "-s", "myapp.nginx_version=2"}
		if version != "" {
			cmd = append(cmd, "--stack-version", version)
		}
		icmd.RunCmd(icmd.Cmd{
			Command: cmd,
			Dir:     dir.Path(),
		}).Assert(t, icmd.Success)

		chart := golden.Get(t, dir.Join("helm.chart/Chart.yaml"))
		values := golden.Get(t, dir.Join("helm.chart/values.yaml"))
		stack := golden.Get(t, dir.Join("helm.chart/templates/stack.yaml"))
		golden.Assert(t, string(chart), "helm-expected.chart/Chart.yaml", "chart file is wrong")
		golden.Assert(t, string(values), "helm-expected.chart/values.yaml", "values file is wrong")
		golden.Assert(t, string(stack), "helm-expected.chart/templates/stack"+version+".yaml", "stack file is wrong")

	}
}

func TestHelmInvalidStackVersionBinary(t *testing.T) {
	icmd.RunCommand(dockerApp, "helm", "testdata/helm", "--stack-version", "foobar").Assert(t, icmd.Expected{
		ExitCode: 1,
		Err:      `Error: invalid stack version "foobar" (accepted values: v1beta1, v1beta2)`,
	})
}

func TestSplitMergeBinary(t *testing.T) {
	icmd.RunCommand(dockerApp, "merge", "testdata/render/envvariables/my.dockerapp", "-o", "remerged.dockerapp").Assert(t, icmd.Success)
	defer os.Remove("remerged.dockerapp")
	// test that inspect works on single-file
	result := icmd.RunCommand(dockerApp, "inspect", "remerged").Assert(t, icmd.Success)
	golden.Assert(t, result.Combined(), "envvariables-inspect.golden")
	// split it
	icmd.RunCommand(dockerApp, "split", "remerged", "-o", "split.dockerapp").Assert(t, icmd.Success)
	defer os.RemoveAll("split.dockerapp")
	result = icmd.RunCommand(dockerApp, "inspect", "remerged").Assert(t, icmd.Success)
	golden.Assert(t, result.Combined(), "envvariables-inspect.golden")
	// test inplace
	icmd.RunCommand(dockerApp, "merge", "split")
	icmd.RunCommand(dockerApp, "split", "split")
}

func TestURLBinary(t *testing.T) {
	url := "https://raw.githubusercontent.com/docker/app/v0.4.1/examples/hello-world/hello-world.dockerapp"
	result := icmd.RunCommand(dockerApp, "inspect", url).Assert(t, icmd.Success)
	golden.Assert(t, result.Combined(), "helloworld-inspect.golden")
}

func TestWithRegistry(t *testing.T) {
	r := startRegistry(t)
	defer r.Stop(t)
	registry := r.GetAddress(t)
	t.Run("image", testImageBinary(registry))
	t.Run("fork", testForkBinary(registry))
}

func testImageBinary(registry string) func(*testing.T) {
	return func(t *testing.T) {
		// push to a registry
		icmd.RunCommand(dockerApp, "push", "--namespace", registry+"/myuser", "testdata/render/envvariables/my.dockerapp").Assert(t, icmd.Success)
		icmd.RunCommand(dockerApp, "push", "--namespace", registry+"/myuser", "-t", "latest", "testdata/render/envvariables/my.dockerapp").Assert(t, icmd.Success)
		icmd.RunCommand(dockerApp, "inspect", registry+"/myuser/my.dockerapp:0.1.0").Assert(t, icmd.Success)
		icmd.RunCommand(dockerApp, "inspect", registry+"/myuser/my.dockerapp").Assert(t, icmd.Success)
		icmd.RunCommand(dockerApp, "inspect", registry+"/myuser/my").Assert(t, icmd.Success)
		icmd.RunCommand(dockerApp, "inspect", registry+"/myuser/my:0.1.0").Assert(t, icmd.Success)
		// push a single-file app to a registry
		dir := fs.NewDir(t, "save-prepare-build", fs.WithFile("my.dockerapp", singleFileApp))
		defer dir.Remove()
		icmd.RunCommand(dockerApp, "push", "--namespace", registry+"/myuser", dir.Join("my.dockerapp")).Assert(t, icmd.Success)
	}
}

func testForkBinary(registry string) func(*testing.T) {
	return func(t *testing.T) {
		icmd.RunCommand(dockerApp, "push", "--namespace", registry+"/acmecorp", "testdata/fork/simple").Assert(t, icmd.Success)

		tempDir := fs.NewDir(t, "dockerapptest")
		defer tempDir.Remove()

		icmd.RunCommand(dockerApp, "fork", registry+"/acmecorp/simple.dockerapp:1.1.0-beta1", "acmecorp/scarlet.devil", "-p", tempDir.Path(), "-m", "Remilia Scarlet:remilia@acmecorp.cool").Assert(t, icmd.Success)
		metadata := golden.Get(t, tempDir.Join("scarlet.devil.dockerapp", "metadata.yml"))
		golden.Assert(t, string(metadata), "expected-fork-metadata.golden")

		icmd.RunCommand(dockerApp, "fork", registry+"/acmecorp/simple.dockerapp:1.1.0-beta1", "-p", tempDir.Path(), "-m", "Remilia Scarlet:remilia@acmecorp.cool").Assert(t, icmd.Success)
		metadata2 := golden.Get(t, tempDir.Join("simple.dockerapp", "metadata.yml"))
		golden.Assert(t, string(metadata2), "expected-fork-metadata-no-rename.golden")
	}
}
