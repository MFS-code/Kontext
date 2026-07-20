import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import json from "highlight.js/lib/languages/json";
import yaml from "highlight.js/lib/languages/yaml";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Link } from "react-router-dom";
import type { Components } from "react-markdown";

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("json", json);
hljs.registerLanguage("yaml", yaml);

type MarkdownProps = {
  source: string;
};

function isInternalPath(href: string): boolean {
  return href.startsWith("/") && !href.startsWith("//");
}

function highlightLanguage(className: string | undefined): string | undefined {
  const language = className?.match(/^language-(\S+)$/)?.[1];
  switch (language) {
    case "bash":
    case "sh":
    case "shell":
      return "bash";
    case "json":
      return "json";
    case "yaml":
    case "yml":
      return "yaml";
    default:
      return undefined;
  }
}

const components: Components = {
  a({ href, children }) {
    if (href && isInternalPath(href)) {
      return <Link to={href}>{children}</Link>;
    }
    return (
      <a
        href={href}
        target={href?.startsWith("http") ? "_blank" : undefined}
        rel={href?.startsWith("http") ? "noreferrer noopener" : undefined}
      >
        {children}
      </a>
    );
  },
  code({ className, children, node: _node, ...props }) {
    const text = String(children).replace(/\n$/, "");
    const isBlock = Boolean(className) || text.includes("\n");
    if (!isBlock) {
      return (
        <code className="inline-code" {...props}>
          {children}
        </code>
      );
    }
    const language = highlightLanguage(className);
    if (language) {
      const highlighted = hljs.highlight(text, { language }).value;
      return (
        <code
          className={`${className ?? ""} hljs`.trim()}
          dangerouslySetInnerHTML={{ __html: highlighted }}
          {...props}
        />
      );
    }
    return (
      <code className={className} {...props}>
        {children}
      </code>
    );
  },
};

export function Markdown({ source }: MarkdownProps) {
  return (
    <div className="markdown">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {source}
      </ReactMarkdown>
    </div>
  );
}
