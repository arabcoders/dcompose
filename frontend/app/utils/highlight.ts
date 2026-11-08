type Highlighter = (typeof import('highlight.js/lib/core'))['default']

let highlighterPromise: Promise<Highlighter> | null = null

export const loadHighlighter = (): Promise<Highlighter> => {
  if (null === highlighterPromise) {
    highlighterPromise = Promise.all([
      import('highlight.js/lib/core'),
      import('highlight.js/lib/languages/yaml'),
      import('highlight.js/lib/languages/ini'),
      import('highlight.js/styles/atom-one-dark.css'),
    ]).then(([{ default: hljs }, { default: yaml }, { default: ini }]) => {
      hljs.registerLanguage('yaml', yaml)
      hljs.registerLanguage('ini', ini)
      return hljs
    })
  }

  return highlighterPromise
}
