import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { CodeBlock, codeLanguage } from "@/components/code-block";
import { MermaidBlock } from "@/components/mermaid-block";

const components: Components = {
  // A fenced block arrives as <pre><code class="language-x">; inline code has no pre and stays as it is.
  pre({ node, children }) {
    const code = node?.children[0];
    if (code?.type !== "element" || code.tagName !== "code") return <pre>{children}</pre>;
    const text = code.children.map((c) => (c.type === "text" ? c.value : "")).join("").replace(/\n$/, "");
    const lang = codeLanguage(code.properties.className);
    return lang === "mermaid" ? <MermaidBlock source={text} /> : <CodeBlock code={text} lang={lang} />;
  },
  // Web links leave the app for a new tab, without handing it this window.
  a({ node: _node, href, ...props }) {
    const external = /^https?:\/\//i.test(href ?? "");
    return <a href={href} {...props} {...(external && { target: "_blank", rel: "noopener noreferrer" })} />;
  },
};

/** An agent's message, rendered: headings, lists, tables, code. Raw HTML in it is shown as text, never run. */
export function Markdown({ children }: { children: string }) {
  return (
    <div className="prose prose-sm dark:prose-invert prose-pre:bg-muted prose-pre:text-foreground prose-code:before:content-none prose-code:after:content-none max-w-none wrap-anywhere">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {children}
      </ReactMarkdown>
    </div>
  );
}
