package plugin

import (
	"os"
	"strings"
	"testing"

	"github.com/kelseyhightower/envconfig"
)

func TestBuildToolWrapperSetting(t *testing.T) {
	for _, setting := range []struct {
		name  string
		value string
		unset bool
	}{
		{name: "unset", unset: true},
		{name: "empty"},
		{name: "true", value: "true"},
		{name: "false", value: "false"},
	} {
		t.Run(setting.name, func(t *testing.T) {
			value := setting.value
			t.Setenv("PLUGIN_USE_WRAPPER", value)
			if setting.unset {
				if err := os.Unsetenv("PLUGIN_USE_WRAPPER"); err != nil {
					t.Fatal(err)
				}
			}
			var parsed Args
			if err := envconfig.Process("", &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.UseWrapper != value {
				t.Fatalf("wrapper setting = %q, want %q", parsed.UseWrapper, value)
			}

			for _, tool := range []string{GradleCmd, MvnCmd} {
				for _, command := range []string{"", "build", Publish} {
					t.Run(tool+"/"+command, func(t *testing.T) {
						args := Args{
							BuildTool:   tool,
							Command:     command,
							UseWrapper:  parsed.UseWrapper,
							URL:         RtUrlTestStr,
							AccessToken: RtAccessToken,
							GradleTasks: "clean build",
							MvnGoals:    "clean install",
						}
						commands, err := getBuildToolCommands(args)
						if err != nil {
							t.Fatal(err)
						}
						if len(commands) != 3 {
							t.Fatalf("got %d commands, want config, build-tool config and build", len(commands))
						}
						wantCount := 0
						if value != "" {
							wantCount = 1
						}
						count := 0
						for i, argv := range commands {
							for _, arg := range argv {
								if !strings.HasPrefix(arg, "--use-wrapper=") {
									continue
								}
								count++
								if i != 1 || arg != "--use-wrapper="+value {
									t.Fatalf("unexpected wrapper flag %q on command %d", arg, i)
								}
							}
						}
						if count != wantCount {
							t.Fatalf("got %d wrapper flags, want %d", count, wantCount)
						}
					})
				}
			}
		})
	}
}
