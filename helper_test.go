package main

// root 助手的端到端测试：在临时目录里搭假 sysfs + 假 debugfs，
// 用普通用户跑真实的 bash 助手，验证「全量校验 → 干跑/执行」逻辑。
// 助手只在显式设置了 DP_RESET_SYSFS_ROOT（测试用）时才允许非 root 运行。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootHelper(t *testing.T) {
	helperData, err := assetsFS.ReadFile("assets/dp-reset-root")
	if err != nil {
		t.Fatalf("read embedded helper: %v", err)
	}
	tmp := t.TempDir()
	helper := filepath.Join(tmp, "dp-reset-root")
	if err := os.WriteFile(helper, helperData, 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	f := newFakeSysfs(t)
	f.addGPU("card0", "0000:05:00.0", "amdgpu", "1002:67DF", true)
	f.addGPU("card1", "0000:00:02.0", "i915", "8086:7D55", false)
	f.addConnector("card0", "DP-4", "connected", "enabled", "On")
	f.addConnector("card0", "DP-5", "disconnected", "disabled", "Off")
	f.addConnector("card0", "HDMI-A-1", "connected", "enabled", "On")
	f.addConnector("card1", "DP-1", "connected", "enabled", "On")

	// 假 debugfs：只有 card0 的 DP-4 有可写的 trigger_hotplug。
	dbg := filepath.Join(tmp, "debugfs")
	trigger := filepath.Join(dbg, "dri", "0000:05:00.0", "DP-4", "trigger_hotplug")
	if err := os.MkdirAll(filepath.Dir(trigger), 0o755); err != nil {
		t.Fatalf("mkdir debugfs: %v", err)
	}
	if err := os.WriteFile(trigger, []byte("0\n"), 0o644); err != nil {
		t.Fatalf("write trigger: %v", err)
	}

	env := append(os.Environ(),
		"DP_RESET_SYSFS_ROOT="+f.root,
		"DP_RESET_DEBUGFS_ROOT="+dbg,
		"DP_RESET_KEEP_OFF=0",
	)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{helper}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// 1) 干跑：校验通过、不写文件、中文输出。
	out, err := run("--dry-run", "--lang=zh_CN", "card0-DP-4")
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "干跑") {
		t.Errorf("dry-run output should be Chinese, got:\n%s", out)
	}
	if body, _ := os.ReadFile(trigger); string(body) != "0\n" {
		t.Errorf("dry-run must not write debugfs, file = %q", body)
	}

	// 2) --keep-off=N 参数可用，且真跑（KEEP_OFF=0）最终应写入 1。
	out, err = run("--lang=en", "--keep-off=0", "card0-DP-4")
	if err != nil {
		t.Fatalf("real run failed: %v\n%s", err, out)
	}
	if body, _ := os.ReadFile(trigger); string(body) != "1\n" {
		t.Errorf("trigger_hotplug should end with 1, got %q", body)
	}

	// 3) 各类非法参数都必须整体拒绝（exit 2）。
	// 显式 --lang=en，避免测试结果被运行环境的 LANG 影响。
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--dry-run", "--lang=en", "card0-HDMI-A-1"}, "expected cardN-DP-N"},
		{[]string{"--dry-run", "--lang=en", "card0-DP-5"}, "not connected"},
		{[]string{"--dry-run", "--lang=en", "card1-DP-1"}, "not an amdgpu"},
		{[]string{"--dry-run", "--lang=en", "card9-DP-4"}, "not present"},
		{[]string{"--dry-run", "--lang=en", "DP-4"}, "expected cardN-DP-N"},
		{[]string{"--dry-run", "--lang=en", "card0-DP-4-1"}, "expected cardN-DP-N"},
		{[]string{"--dry-run", "--lang=en", "--keep-off=abc", "card0-DP-4"}, "must be a number"},
	}
	for _, c := range cases {
		out, err := run(c.args...)
		if err == nil {
			t.Errorf("args %v should fail, output:\n%s", c.args, out)
			continue
		}
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
			t.Errorf("args %v: exit = %v, want 2 (out:\n%s)", c.args, err, out)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("args %v: output should contain %q, got:\n%s", c.args, c.want, out)
		}
	}

	// 4) 多屏一次授权：一次传两个接口（另一个不存在应整体拒绝，不写任何东西）。
	if _, err := os.Stat(trigger); err != nil {
		t.Fatalf("trigger disappeared: %v", err)
	}
	out, err = run("--dry-run", "--lang=en", "card0-DP-4", "card1-DP-1")
	if err == nil {
		t.Errorf("mixed valid/invalid args should be rejected, output:\n%s", out)
	}
}
