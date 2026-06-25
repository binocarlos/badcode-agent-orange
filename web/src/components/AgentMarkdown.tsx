// Markdown renderer for agent messages.
// Copied from frontend/src/components/agent/AgentMarkdown.tsx.
// Uses react-markdown + remark-gfm. No Platinum-specific dependencies.
// CSS is inlined via sx prop / style instead of a .css import to avoid
// bundler configuration requirements. See ../../docs/90-provenance-map.md.

import React from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

interface AgentMarkdownProps {
  content: string
}

export default function AgentMarkdown({ content }: AgentMarkdownProps) {
  return (
    <div
      className="agent-markdown"
      style={{
        fontSize: '0.8125rem',
        lineHeight: 1.6,
      }}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ children, href, ...props }) => (
            <a href={href} target="_blank" rel="noopener noreferrer" {...props}>
              {children}
            </a>
          ),
          table: ({ children, ...props }) => (
            <div style={{ overflowX: 'auto' }}>
              <table {...props}>{children}</table>
            </div>
          ),
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
