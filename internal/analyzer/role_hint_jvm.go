// role_hint_jvm.go —— Java/Kotlin 仓库角色推断:简单字符串匹配 pom.xml / build.gradle(无 XML 解析)。
//
//	<packaging>jar</packaging> 且无 spring-web / spring-boot-starter-web → common-lib
//	有 spring-cloud-gateway / spring-cloud-zuul → gateway
//	有 spring-boot-starter-web / spring-webflux → backend
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

func roleFromJava(repoPath string) RoleHint {
	pom, err := os.ReadFile(filepath.Join(repoPath, "pom.xml"))
	if err != nil {
		// gradle 项目暂不深扫,看 build.gradle 里的关键字
		gradle, gerr := os.ReadFile(filepath.Join(repoPath, "build.gradle"))
		if gerr != nil {
			gradle, gerr = os.ReadFile(filepath.Join(repoPath, "build.gradle.kts"))
		}
		if gerr == nil {
			return roleFromJavaText(string(gradle), "build.gradle")
		}
		return RoleHint{}
	}
	return roleFromJavaText(string(pom), "pom.xml")
}

func roleFromJavaText(text, filename string) RoleHint {
	low := strings.ToLower(text)
	if strings.Contains(low, "spring-cloud-starter-gateway") || strings.Contains(low, "spring-cloud-gateway") {
		return RoleHint{Role: "gateway", Reason: filename + " 含 spring-cloud-gateway"}
	}
	if strings.Contains(low, "spring-cloud-zuul") {
		return RoleHint{Role: "gateway", Reason: filename + " 含 zuul"}
	}
	if strings.Contains(low, "spring-boot-starter-web") || strings.Contains(low, "spring-webflux") {
		return RoleHint{Role: "backend", Reason: filename + " 含 spring-boot-starter-web"}
	}
	if strings.Contains(low, "<packaging>jar</packaging>") &&
		!strings.Contains(low, "spring-boot-starter") &&
		!strings.Contains(low, "spring-web") {
		return RoleHint{Role: "common-lib", Reason: filename + " packaging=jar 且无 web 框架依赖"}
	}
	return RoleHint{}
}
