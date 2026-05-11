// role_hint_php.go —— PHP 仓库角色推断:composer.json + 框架特征文件。
//
//	含 laravel / symfony / yii / thinkphp / phalcon → backend
//	含 nova / horizon / filament(后台框架)→ admin
//	只有 composer.json 无入口 / 无框架 → common-lib
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

func roleFromPHP(repoPath string) RoleHint {
	composerData, err := os.ReadFile(filepath.Join(repoPath, "composer.json"))
	if err != nil {
		return RoleHint{}
	}
	low := strings.ToLower(string(composerData))

	adminMarkers := []string{"laravel/nova", "filament/filament", "symfony/admin-bundle", "easycorp/easyadmin-bundle", "encore/laravel-admin", "thinkphp/think-admin"}
	for _, m := range adminMarkers {
		if strings.Contains(low, m) {
			return RoleHint{Role: "admin", Reason: "composer.json 含 " + m + "(后台框架)"}
		}
	}

	backFrameworks := []string{"laravel/framework", "symfony/symfony", "symfony/framework-bundle", "yiisoft/yii2", "topthink/framework", "topthink/think", "phalcon", "slim/slim", "cakephp/cakephp", "codeigniter4/framework", "hyperf/hyperf"}
	for _, fw := range backFrameworks {
		if strings.Contains(low, fw) {
			return RoleHint{Role: "backend", Reason: "composer.json 含 " + fw}
		}
	}

	for _, entry := range []string{"public/index.php", "index.php", "artisan"} {
		if _, err := os.Stat(filepath.Join(repoPath, entry)); err == nil {
			return RoleHint{Role: "backend", Reason: "PHP 入口文件: " + entry}
		}
	}

	return RoleHint{Role: "common-lib", Reason: "composer.json 存在但无 web 框架 / 入口"}
}
