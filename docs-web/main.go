package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
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
	outDir := "docs-web"
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, e := range entries {
			if e.Name() != "main.go" {
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

	// 复制 index
	if _, err := os.Stat(filepath.Join(outDir, "docs/README.html")); err == nil {
		copyFile(filepath.Join(outDir, "docs/README.html"), filepath.Join(outDir, "index.html"))
	} else if _, err := os.Stat(filepath.Join(outDir, "README.html")); err == nil {
		copyFile(filepath.Join(outDir, "README.html"), filepath.Join(outDir, "index.html"))
	}

	fmt.Println("🎉 文档生成完毕！可以直接在浏览器中打开 docs-web/index.html")
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

	sb.WriteString("<h3>主目录</h3><ul>")
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
	formatter.WriteCSS(f, styles.Get("github"))
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

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s - GatewayWorker-Go 文档</title>
    <link rel="stylesheet" href="%sgithub-markdown.css">
    <link rel="stylesheet" href="%schroma.css">
    <style>
        body {
            display: flex;
            margin: 0;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
            background-color: #fff;
            color: #24292f;
        }
        .sidebar {
            width: 280px;
            height: 100vh;
            overflow-y: auto;
            background: #f6f8fa;
            border-right: 1px solid #d0d7de;
            padding: 20px;
            position: fixed;
            box-sizing: border-box;
        }
        .sidebar h3 {
            margin-top: 1.5em;
            margin-bottom: 0.5em;
            font-size: 14px;
            color: #57606a;
            text-transform: uppercase;
        }
        .sidebar h3:first-child { margin-top: 0; }
        .sidebar ul {
            list-style: none;
            padding: 0;
            margin: 0;
        }
        .sidebar li {
            margin-bottom: 6px;
        }
        .sidebar a {
            text-decoration: none;
            color: #0969da;
            font-size: 14px;
            display: block;
            padding: 4px 8px;
            border-radius: 6px;
        }
        .sidebar a:hover {
            background-color: #eaf0f6;
            text-decoration: underline;
        }
        .content {
            margin-left: 280px;
            padding: 40px;
            max-width: 900px;
            width: 100%%;
            box-sizing: border-box;
        }
        .markdown-body {
            box-sizing: border-box;
            min-width: 200px;
            max-width: 980px;
            margin: 0 auto;
            padding: 45px;
        }
        @media (max-width: 767px) {
            .markdown-body { padding: 15px; }
            body { flex-direction: column; }
            .sidebar { width: 100%%; height: auto; position: relative; border-right: none; border-bottom: 1px solid #d0d7de; }
            .content { margin-left: 0; }
        }
    </style>
</head>
<body>
    <div class="sidebar">
        %s
    </div>
    <div class="content markdown-body">
        %s
    </div>
</body>
</html>`, title, resourcePrefix, resourcePrefix, navStr, content)
}
