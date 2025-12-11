/**
 * CodeMirror Tokyo Night Theme
 * Custom theme matching the application's Tokyo Night palette
 */

import { EditorView } from '@codemirror/view';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { tags } from '@lezer/highlight';

// Tokyo Night color palette
const colors = {
  bg: '#1a1b26',
  bgDark: '#16161e',
  bgHighlight: '#24283b',
  bgSelection: '#33467c',
  fg: '#a9b1d6',
  fgDark: '#565f89',
  comment: '#565f89',
  blue: '#7aa2f7',
  cyan: '#7dcfff',
  green: '#9ece6a',
  magenta: '#bb9af7',
  orange: '#ff9e64',
  red: '#f7768e',
  yellow: '#e0af68',
  white: '#c0caf5',
};

// Editor theme (background, cursor, selection, etc.)
export const tokyoNightTheme = EditorView.theme(
  {
    '&': {
      backgroundColor: colors.bg,
      color: colors.fg,
    },
    '.cm-content': {
      caretColor: colors.blue,
      fontFamily: '"JetBrains Mono", "Fira Code", monospace',
      fontSize: '13px',
      lineHeight: '1.6',
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: colors.blue,
      borderLeftWidth: '2px',
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
      {
        backgroundColor: colors.bgSelection,
      },
    '.cm-activeLine': {
      backgroundColor: colors.bgHighlight,
    },
    '.cm-activeLineGutter': {
      backgroundColor: colors.bgHighlight,
    },
    '.cm-gutters': {
      backgroundColor: colors.bgDark,
      color: colors.fgDark,
      border: 'none',
      borderRight: `1px solid ${colors.bgHighlight}`,
    },
    '.cm-lineNumbers .cm-gutterElement': {
      padding: '0 8px 0 12px',
    },
    '.cm-foldGutter': {
      color: colors.fgDark,
    },
    '.cm-matchingBracket, .cm-nonmatchingBracket': {
      backgroundColor: colors.bgSelection,
      outline: `1px solid ${colors.blue}`,
    },
    '.cm-searchMatch': {
      backgroundColor: colors.yellow + '40',
      outline: `1px solid ${colors.yellow}`,
    },
    '.cm-searchMatch.cm-searchMatch-selected': {
      backgroundColor: colors.yellow + '60',
    },
    '.cm-tooltip': {
      backgroundColor: colors.bgDark,
      border: `1px solid ${colors.bgHighlight}`,
      color: colors.fg,
    },
    '.cm-tooltip-autocomplete': {
      '& > ul > li[aria-selected]': {
        backgroundColor: colors.bgSelection,
        color: colors.white,
      },
    },
    '.cm-panels': {
      backgroundColor: colors.bgDark,
      color: colors.fg,
    },
    '.cm-panels.cm-panels-top': {
      borderBottom: `1px solid ${colors.bgHighlight}`,
    },
    '.cm-panels.cm-panels-bottom': {
      borderTop: `1px solid ${colors.bgHighlight}`,
    },
    '.cm-scroller': {
      fontFamily: '"JetBrains Mono", "Fira Code", monospace',
    },
  },
  { dark: true }
);

// Syntax highlighting
export const tokyoNightHighlightStyle = HighlightStyle.define([
  // Comments
  { tag: tags.comment, color: colors.comment, fontStyle: 'italic' },
  { tag: tags.lineComment, color: colors.comment, fontStyle: 'italic' },
  { tag: tags.blockComment, color: colors.comment, fontStyle: 'italic' },

  // Strings
  { tag: tags.string, color: colors.green },
  { tag: tags.special(tags.string), color: colors.cyan },

  // Numbers
  { tag: tags.number, color: colors.orange },
  { tag: tags.integer, color: colors.orange },
  { tag: tags.float, color: colors.orange },

  // Boolean
  { tag: tags.bool, color: colors.orange },
  { tag: tags.null, color: colors.orange },

  // Keywords
  { tag: tags.keyword, color: colors.magenta },
  { tag: tags.controlKeyword, color: colors.magenta },
  { tag: tags.operatorKeyword, color: colors.magenta },
  { tag: tags.definitionKeyword, color: colors.magenta },
  { tag: tags.moduleKeyword, color: colors.magenta },

  // Types
  { tag: tags.typeName, color: colors.cyan },
  { tag: tags.className, color: colors.cyan },
  { tag: tags.namespace, color: colors.cyan },

  // Variables and properties
  { tag: tags.variableName, color: colors.fg },
  { tag: tags.definition(tags.variableName), color: colors.blue },
  { tag: tags.propertyName, color: colors.blue },
  { tag: tags.definition(tags.propertyName), color: colors.blue },

  // Functions
  { tag: tags.function(tags.variableName), color: colors.blue },
  { tag: tags.function(tags.propertyName), color: colors.blue },

  // Operators
  { tag: tags.operator, color: colors.cyan },
  { tag: tags.arithmeticOperator, color: colors.cyan },
  { tag: tags.logicOperator, color: colors.cyan },
  { tag: tags.compareOperator, color: colors.cyan },

  // Punctuation
  { tag: tags.punctuation, color: colors.fgDark },
  { tag: tags.separator, color: colors.fgDark },
  { tag: tags.bracket, color: colors.fg },
  { tag: tags.brace, color: colors.fg },
  { tag: tags.paren, color: colors.fg },
  { tag: tags.squareBracket, color: colors.fg },
  { tag: tags.angleBracket, color: colors.fg },

  // GraphQL specific
  { tag: tags.labelName, color: colors.blue }, // Field names
  { tag: tags.atom, color: colors.orange }, // Enums, booleans
  { tag: tags.attributeName, color: colors.magenta }, // Directives
  { tag: tags.attributeValue, color: colors.green }, // Directive values

  // Tags and attributes (for HTML/JSX if needed)
  { tag: tags.tagName, color: colors.red },
  { tag: tags.attributeName, color: colors.magenta },
  { tag: tags.attributeValue, color: colors.green },

  // Invalid
  { tag: tags.invalid, color: colors.red, textDecoration: 'underline wavy' },
]);

// Combined theme extension
export const tokyoNight = [
  tokyoNightTheme,
  syntaxHighlighting(tokyoNightHighlightStyle),
];
