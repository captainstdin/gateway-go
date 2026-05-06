package main

import (
	"bytes"
	"fmt"
	"io"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	renderer_html "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark-highlighting/v2"
)

var linkRegex = regexp.MustCompile(`href="([^"]+)\.md(#.*?)?"`)

func main() {
	cwd, err := os.Getwd()
	if err == nil && filepath.Base(cwd) == "docs-web" {
		if err := os.Chdir(".."); err != nil {
			fmt.Println("⚠️ 无法切换到项目根目录，请在正确的目录下运行。")
			os.Exit(1)
		}
	} else if _, err := os.Stat("docs-web/templates/layout.html"); os.IsNotExist(err) {
		fmt.Println("⚠️ 请在 gatewayworker-go 项目根目录下运行此脚本 (或者在 docs-web 目录下运行): go run docs-web/main.go")
		os.Exit(1)
	}

	if len(os.Args) > 1 {
		buildDocs()
	} else {
		startServer("docs-web")
	}
}

func startServer(dir string) {
	port := "8080"
	fs := http.FileServer(http.Dir(dir))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fs.ServeHTTP(w, r)
	})

	addr := ":" + port
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("⚠️  端口 %s 可能已被占用，正在尝试随机分配可用端口...\n", port)
		listener, err = net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("❌ 无法监听任何端口: %v", err)
		}
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	fmt.Printf("\n🚀 Web 服务器启动成功！\n")
	fmt.Printf("👉 访问地址: http://localhost:%d\n", actualPort)
	fmt.Printf("📂 正在代理目录: %s\n", dir)
	fmt.Println("Press Ctrl+C to stop")

	err = http.Serve(listener, nil)
	if err != nil {
		log.Fatalf("❌ 服务器运行异常退出: %v", err)
	}
}

func buildDocs() {
	outDir := "docs-web"
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, e := range entries {
			if e.Name() != "main.go" && e.Name() != "templates" {
				os.RemoveAll(filepath.Join(outDir, e.Name()))
			}
		}
	} else {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			panic(err)
		}
	}

	fmt.Println("🚀 开始生成静态文档...")

	// 1. 生成高亮 CSS
	generateChromaCSS(filepath.Join(outDir, "chroma.css"))

	// 2. 下载 GitHub Markdown CSS
	downloadCSS("https://cdnjs.cloudflare.com/ajax/libs/github-markdown-css/5.5.0/github-markdown.min.css", filepath.Join(outDir, "github-markdown.css"))

	// 3. 收集所有 MD 文件
	var mdFiles []string
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "docs-web" || name == ".idea" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			// 只收集 docs/ 目录下的，以及 pkg/ 下的，还有根目录的
			if strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, "pkg/") || !strings.Contains(path, "/") {
				mdFiles = append(mdFiles, path)
			}
		}
		return nil
	})

	// 4. 构建左侧导航
	navHTML := buildNavigation(mdFiles)

	// 5. 初始化 Goldmark
	mdParser := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
				highlighting.WithFormatOptions(
					html.WithClasses(true),
				),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			renderer_html.WithUnsafe(), // 允许嵌入的 HTML 标签
		),
	)

	// 6. 转换并写入
	for _, mdPath := range mdFiles {
		content, err := os.ReadFile(mdPath)
		if err != nil {
			continue
		}

		var buf bytes.Buffer
		if err := mdParser.Convert(content, &buf); err != nil {
			fmt.Printf("❌ 转换失败 %s: %v\n", mdPath, err)
			continue
		}

		// 替换内部 .md 链接为 .html
		htmlContent := linkRegex.ReplaceAllString(buf.String(), `href="$1.html$2"`)

		// 计算相对根目录的深度，用于引入 CSS
		depth := strings.Count(mdPath, "/")
		prefix := ""
		for i := 0; i < depth; i++ {
			prefix += "../"
		}
		if prefix == "" {
			prefix = "./"
		}

		title := filepath.Base(mdPath)
		fullHTML := wrapHTML(title, navHTML, htmlContent, prefix)

		outPath := filepath.Join(outDir, strings.TrimSuffix(mdPath, ".md")+".html")
		os.MkdirAll(filepath.Dir(outPath), 0755)
		if err := os.WriteFile(outPath, []byte(fullHTML), 0644); err != nil {
			fmt.Printf("❌ 写入失败 %s: %v\n", outPath, err)
		} else {
			fmt.Printf("✅ 已生成: %s\n", outPath)
		}
	}

	// 生成自定义首页
	generateIndexHTML(outDir, navHTML)

	fmt.Println("🎉 文档生成完毕！可以直接在浏览器中打开 docs-web/index.html")
}

func generateIndexHTML(outDir, navHTML string) {
	// 获取 Git 信息
	hash, _ := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	branch, _ := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	date, _ := exec.Command("git", "log", "-1", "--format=%cd", "--date=format:%Y-%m-%d %H:%M:%S").Output()
	gitURL := getGitRemoteURL()
	
	// 统计代码行数
	var totalLines int
	var fileCount int
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.Contains(path, "vendor/") {
			content, err := os.ReadFile(path)
			if err == nil {
				totalLines += bytes.Count(content, []byte{'\n'}) + 1
				fileCount++
			}
		}
		return nil
	})

	tmpl, err := template.ParseFiles("docs-web/templates/dashboard.html")
	if err != nil {
		fmt.Printf("⚠️ 无法加载仪表盘模板: %v\n", err)
		return
	}

	var buf bytes.Buffer
	data := struct {
		Branch     string
		Hash       string
		Date       string
		GitURL     string
		FileCount  int
		TotalLines int
	}{
		Branch:     strings.TrimSpace(string(branch)),
		Hash:       strings.TrimSpace(string(hash)),
		Date:       strings.TrimSpace(string(date)),
		GitURL:     gitURL,
		FileCount:  fileCount,
		TotalLines: totalLines,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		fmt.Printf("⚠️ 渲染仪表盘失败: %v\n", err)
		return
	}

	fullHTML := wrapHTML("首页", navHTML, buf.String(), "./")
	os.WriteFile(filepath.Join(outDir, "index.html"), []byte(fullHTML), 0644)
}

func buildNavigation(files []string) string {
	var sb strings.Builder
	
	// 对文件进行分组
	var rootFiles, docsFiles, pkgFiles []string
	for _, f := range files {
		if strings.HasPrefix(f, "docs/") {
			docsFiles = append(docsFiles, f)
		} else if strings.HasPrefix(f, "pkg/") {
			pkgFiles = append(pkgFiles, f)
		} else {
			rootFiles = append(rootFiles, f)
		}
	}

	sb.WriteString(`<h3><a href="/index.html">主目录</a></h3><ul>`)
	for _, f := range rootFiles {
		sb.WriteString(fmt.Sprintf(`<li><a href="/%s">%s</a></li>`, toHTML(f), filepath.Base(f)))
	}
	sb.WriteString("</ul>")

	sb.WriteString("<h3>使用文档 (docs)</h3><ul>")
	// 手动调整顺序，优先展示常用的
	priority := []string{"docs/cheatsheet.md", "docs/README.md", "docs/architecture.md", "docs/usage.md"}
	added := make(map[string]bool)
	for _, p := range priority {
		for _, f := range docsFiles {
			if f == p {
				sb.WriteString(fmt.Sprintf(`<li><a href="/%s">%s</a></li>`, toHTML(f), filepath.Base(f)))
				added[f] = true
			}
		}
	}
	for _, f := range docsFiles {
		if !added[f] {
			sb.WriteString(fmt.Sprintf(`<li><a href="/%s">%s</a></li>`, toHTML(f), filepath.Base(f)))
		}
	}
	sb.WriteString("</ul>")

	sb.WriteString("<h3>包文档 (pkg)</h3><ul>")
	for _, f := range pkgFiles {
		sb.WriteString(fmt.Sprintf(`<li><a href="/%s">%s</a></li>`, toHTML(f), filepath.Dir(f)))
	}
	sb.WriteString("</ul>")

	return sb.String()
}

func toHTML(mdPath string) string {
	return strings.TrimSuffix(mdPath, ".md") + ".html"
}

func generateChromaCSS(outPath string) {
	f, err := os.Create(outPath)
	if err != nil {
		return
	}
	defer f.Close()
	formatter := html.New(html.WithClasses(true))
	
	f.WriteString("@media (prefers-color-scheme: light) {\n")
	formatter.WriteCSS(f, styles.Get("github"))
	f.WriteString("}\n\n@media (prefers-color-scheme: dark) {\n")
	
	darkStyle := styles.Get("github-dark")
	if darkStyle.Name == "fallback" {
		darkStyle = styles.Get("monokai")
	}
	formatter.WriteCSS(f, darkStyle)
	f.WriteString("}\n")
}

func downloadCSS(url, outPath string) {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("⚠️ 警告：无法下载 CSS %s\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	os.WriteFile(outPath, body, 0644)
}

func copyFile(src, dst string) {
	data, _ := os.ReadFile(src)
	os.WriteFile(dst, data, 0644)
}

func wrapHTML(title, nav, content, resourcePrefix string) string {
	// 将导航里的绝对路径 / 开头替换为当前前缀
	navStr := strings.ReplaceAll(nav, `href="/`, `href="`+resourcePrefix)

	tmpl, err := template.ParseFiles("docs-web/templates/layout.html")
	if err != nil {
		fmt.Printf("⚠️ 无法加载布局模板: %v\n", err)
		return content
	}

	var buf bytes.Buffer
	data := struct {
		Title          string
		ResourcePrefix string
		NavHTML        template.HTML
		Content        template.HTML
	}{
		Title:          title,
		ResourcePrefix: resourcePrefix,
		NavHTML:        template.HTML(navStr),
		Content:        template.HTML(content),
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		fmt.Printf("⚠️ 渲染布局模板失败: %v\n", err)
		return content
	}

	return buf.String()
}

func getGitRemoteURL() string {
	cmd := exec.Command("git", "remote", "-v")
	out, err := cmd.Output()
	if err != nil {
		return "#"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "#"
	}
	parts := strings.Fields(lines[0])
	if len(parts) >= 2 {
		url := parts[1]
		if strings.HasPrefix(url, "ssh://") {
			url = strings.TrimPrefix(url, "ssh://")
			if strings.Contains(url, "@") {
				url = strings.SplitN(url, "@", 2)[1]
			}
			slashIdx := strings.Index(url, "/")
			if slashIdx != -1 {
				hostPart := url[:slashIdx]
				if colonIdx := strings.Index(hostPart, ":"); colonIdx != -1 {
					hostPart = hostPart[:colonIdx]
				}
				url = hostPart + url[slashIdx:]
			}
			url = "https://" + url
		} else if strings.HasPrefix(url, "git@") {
			url = strings.Replace(url, ":", "/", 1)
			url = strings.Replace(url, "git@", "https://", 1)
		}
		if strings.HasSuffix(url, ".git") {
			url = strings.TrimSuffix(url, ".git")
		}
		return url
	}
	return "#"
}
