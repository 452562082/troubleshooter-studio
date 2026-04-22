package main

import (
	"flag"
	"fmt"

	"github.com/xiaolong/troubleshooter-studio/internal/skillscaffold"
)

func runSkillNew(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("skill name required: tshoot skill new <name> [flags]")
	}
	name := args[0]
	fs := flag.NewFlagSet("skill new", flag.ExitOnError)
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁或 CWD 的 templates/)")
	desc := fs.String("description", "", "skill 一行描述，填 front matter 的 description")
	withScripts := fs.Bool("with-scripts", false, "创建 scripts/ 目录 + README 占位")
	withRefs := fs.Bool("with-references", false, "创建 references/ 目录 + README 占位")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}
	dst, err := skillscaffold.New(skillscaffold.Options{
		TemplateRoot: tr,
		Name:         name,
		Description:  *desc,
		WithScripts:  *withScripts,
		WithRefs:     *withRefs,
	})
	if err != nil {
		return err
	}
	fmt.Printf("[ok] scaffolded skill → %s\n", dst)
	fmt.Println("下一步:")
	fmt.Println("  1) 编辑 SKILL.md.tmpl 补全描述与执行流程")
	fmt.Printf("  2) 在 system.yaml 的 generation.skills_whitelist 中加入 \"%s\"\n", name)
	fmt.Println("  3) tshoot plan -i system.yaml 预览")
	return nil
}
