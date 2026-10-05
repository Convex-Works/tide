import { Marked, type Tokens } from 'marked';

export type Rendered = { html: string; toc: { id: string; title: string }[] };

function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/<[^>]+>|`/g, '')
    .replace(/[^a-z0-9 -]/g, '')
    .trim()
    .replace(/\s+/g, '-');
}

function escape(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// Dims shell and YAML comments: a # that starts the line or follows a space.
function highlight(text: string, lang: string): string {
  if (!['sh', 'yaml', 'js'].includes(lang)) return escape(text);
  const marker = lang === 'js' ? '//' : '#';
  return text
    .split('\n')
    .map((line) => {
      const at = line.search(lang === 'js' ? /(^|\s)\/\// : /(^|\s)#/);
      if (at < 0) return escape(line);
      const start = line.startsWith(marker, at) ? at : at + 1;
      return `${escape(line.slice(0, start))}<span class="comment">${escape(line.slice(start))}</span>`;
    })
    .join('\n');
}

/** Renders a docs page, giving each heading an id and collecting the h2s. */
export function render(markdown: string): Rendered {
  const toc: Rendered['toc'] = [];
  const marked = new Marked({
    gfm: true,
    renderer: {
      heading({ tokens, depth, text }: Tokens.Heading) {
        const id = slugify(text);
        if (depth === 2) toc.push({ id, title: text.replace(/`/g, '') });
        return `<h${depth} id="${id}">${this.parser.parseInline(tokens)}</h${depth}>\n`;
      },
      code({ text, lang }: Tokens.Code) {
        const language = (lang ?? '').trim() || 'text';
        return (
          `<div class="code"><div class="code-bar"><span>${escape(language)}</span>` +
          `<button type="button" data-copy>Copy</button></div>` +
          `<pre><code>${highlight(text, language)}</code></pre></div>\n`
        );
      }
    }
  });
  const html = (marked.parse(markdown, { async: false }) as string)
    .replace(/<table>/g, '<div class="table-wrap"><table>')
    .replace(/<\/table>/g, '</table></div>');
  return { html, toc };
}
