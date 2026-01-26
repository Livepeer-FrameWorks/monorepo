<script lang="ts">
  import { onMount } from 'svelte';
  import { browser } from '$app/environment';
  import type { GraphQLSchema } from 'graphql';
  import type { Diagnostic as LintDiagnostic } from '@codemirror/lint';

  // Schema can be a proper GraphQLSchema for autocomplete,
  // or an introspection result (we'll build the schema from it),
  // or null for basic syntax highlighting only
  type SchemaInput = GraphQLSchema | Record<string, unknown> | null;

  interface Props {
    value?: string;
    language?: 'graphql' | 'json';
    schema?: SchemaInput;
    readonly?: boolean;
    placeholder?: string;
    minHeight?: string;
    class?: string;
    onchange?: (value: string) => void;
    onkeydown?: (event: KeyboardEvent) => void;
    onCursorInfo?: (info: CursorInfo | null) => void;
  }

  let {
    value = $bindable(''),
    language = 'graphql',
    schema = null,
    readonly = false,
    placeholder = '',
    minHeight = '200px',
    class: className = '',
    onchange,
    onkeydown,
    onCursorInfo,
  }: Props = $props();

  let container: HTMLDivElement;
  let view: import('@codemirror/view').EditorView | null = null;
  let isInitialized = $state(false);

  // Track if we're updating from external value change
  let isExternalUpdate = false;

  // Store function reference for updateSchema
  let updateSchemaFn: ((view: import('@codemirror/view').EditorView, schema: GraphQLSchema) => void) | null = null;
  let buildClientSchemaFn: typeof import('graphql').buildClientSchema | null = null;
  let getContextAtPositionFn: typeof import('graphql-language-service').getContextAtPosition | null = null;
  let offsetToPositionFn: typeof import('graphql-language-service').offsetToPosition | null = null;
  let _getHoverInformationFn: typeof import('graphql-language-service').getHoverInformation | null = null;
  let _getDiagnosticsFn: typeof import('graphql-language-service').getDiagnostics | null = null;

  // Convert line/character position to string offset
  function positionToOffset(text: string, pos: { line: number; character: number }): number {
    const lines = text.split('\n');
    let offset = 0;
    for (let i = 0; i < pos.line && i < lines.length; i++) {
      offset += lines[i].length + 1; // +1 for newline
    }
    return offset + Math.min(pos.character, lines[pos.line]?.length ?? 0);
  }
  let graphqlSchema: GraphQLSchema | null = null;
  let lastCursorSignature: string | null = null;

  type CursorInfo = {
    kind: 'field' | 'argument' | 'enum' | 'type' | 'directive' | 'variable';
    signature: string;
    description?: string;
    args?: Array<{
      name: string;
      type: string;
      description?: string;
      inputFields?: Array<{ name: string; type: string; description?: string }>;
    }>;
    inputFields?: Array<{ name: string; type: string; description?: string }>;
    enumValues?: Array<{ name: string; description?: string }>;
  };

  onMount(() => {
    if (!browser) return;

    // Initialize editor asynchronously
    initializeEditor();

    return () => {
      view?.destroy();
      view = null;
    };
  });

  // Build GraphQL schema from introspection result
  function buildSchema(schemaInput: SchemaInput): GraphQLSchema | undefined {
    if (!schemaInput || !buildClientSchemaFn) return undefined;

    if (typeof schemaInput === 'object' && 'types' in schemaInput) {
      try {
        // Schema is an introspection result (__schema object)
        return buildClientSchemaFn({
          __schema: schemaInput as unknown as Parameters<typeof buildClientSchemaFn>[0]['__schema']
        });
      } catch (e) {
        console.warn('Failed to build GraphQL schema from introspection:', e);
        return undefined;
      }
    } else if (typeof schemaInput === 'object' && 'getQueryType' in schemaInput) {
      // Already a GraphQLSchema instance
      return schemaInput as GraphQLSchema;
    }

    return undefined;
  }

  async function initializeEditor() {
    // Dynamic imports for CodeMirror
    const { EditorView, keymap, placeholder: placeholderExtension } = await import('@codemirror/view');
    const { EditorState } = await import('@codemirror/state');
    const { defaultKeymap, history, historyKeymap } = await import('@codemirror/commands');
    const { bracketMatching, foldGutter, foldKeymap } = await import('@codemirror/language');
    const { closeBrackets, closeBracketsKeymap, autocompletion, acceptCompletion } = await import('@codemirror/autocomplete');
    const { lineNumbers, highlightActiveLineGutter, highlightActiveLine } = await import('@codemirror/view');
    const { json } = await import('@codemirror/lang-json');
    const { graphql, updateSchema } = await import('cm6-graphql');
    const { tokyoNight } = await import('./codemirrorTheme');
    const { buildClientSchema, GraphQLInputObjectType, GraphQLEnumType, GraphQLList, GraphQLNonNull } = await import('graphql');
    const { getContextAtPosition, offsetToPosition, getHoverInformation, getDiagnostics } = await import('graphql-language-service');
    const { hoverTooltip } = await import('@codemirror/view');
    const { linter, lintGutter } = await import('@codemirror/lint');

    // Store function references for later use
    updateSchemaFn = updateSchema;
    buildClientSchemaFn = buildClientSchema;
    getContextAtPositionFn = getContextAtPosition;
    offsetToPositionFn = offsetToPosition;
    _getHoverInformationFn = getHoverInformation;
    _getDiagnosticsFn = getDiagnostics;

    // Build initial GraphQL schema if available
    graphqlSchema = buildSchema(schema) || null;

    // Build extensions array based on language
    const languageExtension = language === 'graphql'
      ? graphql(graphqlSchema ?? undefined)
      : json();

    const extensions = [
      // Theme
      tokyoNight,

      // Basic editing features
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightActiveLine(),
      history(),
      bracketMatching(),
      closeBrackets(),
      foldGutter(),

      // Enable autocompletion
      autocompletion(),

      // Keymaps
      keymap.of([
        { key: 'Tab', run: acceptCompletion },
        ...defaultKeymap,
        ...historyKeymap,
        ...closeBracketsKeymap,
        ...foldKeymap,
      ]),

      // Placeholder text
      placeholder && placeholderExtension(placeholder),

      // Language support
      languageExtension,

      // GraphQL-specific: Hover tooltips with schema docs
      // Note: Always add extension, callback handles null schema gracefully
      language === 'graphql' && hoverTooltip((view: import('@codemirror/view').EditorView, pos: number) => {
        if (!graphqlSchema || !offsetToPositionFn) return null;

        const docText = view.state.doc.toString();
        const cursorPos = offsetToPositionFn(docText, pos);

        // Get hover information from graphql-language-service
        const hoverInfo = getHoverInformation(graphqlSchema, docText, cursorPos);
        if (!hoverInfo) return null;

        // hoverInfo can be string or array of MarkedString
        const content = Array.isArray(hoverInfo)
          ? hoverInfo.map(h => typeof h === 'string' ? h : h.value).join('\n\n')
          : typeof hoverInfo === 'string' ? hoverInfo : '';

        if (!content.trim()) return null;

        // Find word boundaries for better tooltip positioning
        const line = view.state.doc.lineAt(pos);
        let start = pos;
        let end = pos;
        const text = line.text;

        // Expand to word boundaries
        while (start > line.from && /[\w_]/.test(text[start - line.from - 1])) start--;
        while (end < line.to && /[\w_]/.test(text[end - line.from])) end++;

        return {
          pos: start,
          end,
          above: true,
          create() {
            const dom = document.createElement('div');
            dom.className = 'cm-graphql-hover';

            // Parse markdown-like content (```graphql blocks)
            const parts = content.split(/```(\w*)\n?/);
            let inCode = false;

            parts.forEach((part, i) => {
              if (i % 2 === 1) {
                // This is a language identifier, next part is code
                inCode = true;
                return;
              }

              if (inCode) {
                const code = document.createElement('pre');
                code.className = 'cm-hover-code';
                code.textContent = part.trim();
                dom.appendChild(code);
                inCode = false;
              } else if (part.trim()) {
                const text = document.createElement('div');
                text.className = 'cm-hover-text';
                text.textContent = part.trim();
                dom.appendChild(text);
              }
            });

            return { dom };
          }
        };
      }),

      // GraphQL-specific: Live linting/diagnostics
      // Note: Always add extension, callback handles null schema gracefully
      language === 'graphql' && linter((view: import('@codemirror/view').EditorView) => {
        if (!graphqlSchema) return [];

        const docText = view.state.doc.toString();
        if (!docText.trim()) return [];

        try {
          const rawDiagnostics = getDiagnostics(docText, graphqlSchema);

          return rawDiagnostics.map((d): LintDiagnostic | null => {
            const startLine = d.range?.start?.line ?? 0;
            const startChar = d.range?.start?.character ?? 0;
            const endLine = d.range?.end?.line ?? startLine;
            const endChar = d.range?.end?.character ?? startChar + 1;

            // Convert position to offset
            const from = positionToOffset(docText, { line: startLine, character: startChar });
            const to = positionToOffset(docText, { line: endLine, character: endChar });

            // Ensure valid range
            if (from < 0 || to < 0 || from > docText.length || to > docText.length) return null;

            return {
              from,
              to: Math.max(to, from + 1), // Ensure non-zero width
              severity: d.severity === 1 ? 'error' : 'warning',
              message: d.message,
            };
          }).filter((d): d is LintDiagnostic => d !== null);
        } catch {
          // Silently fail on parse errors during typing
          return [];
        }
      }, { delay: 300 }),

      // GraphQL-specific: Lint gutter (shows icons in margin)
      language === 'graphql' && graphqlSchema && lintGutter(),

      // Read-only mode
      readonly && EditorState.readOnly.of(true),

      // Update listener
      EditorView.updateListener.of((update) => {
        if (update.docChanged && !isExternalUpdate) {
          const newValue = update.state.doc.toString();
          value = newValue;
          onchange?.(newValue);
        }

        if (
          onCursorInfo &&
          language === 'graphql' &&
          graphqlSchema &&
          getContextAtPositionFn &&
          offsetToPositionFn &&
          (update.selectionSet || update.docChanged)
        ) {
          const docText = update.state.doc.toString();
          const cursorOffset = update.state.selection.main.head;
          const cursorPos = offsetToPositionFn(docText, cursorOffset);
          const context = getContextAtPositionFn(docText, cursorPos, graphqlSchema);

          const unwrapInputType = (type: unknown): unknown => {
            let current = type;
            while (current instanceof GraphQLNonNull || current instanceof GraphQLList) {
              current = current.ofType;
            }
            return current;
          };

          const getInputFields = (type: unknown) => {
            const base = unwrapInputType(type);
            if (!base || !(base instanceof GraphQLInputObjectType)) return undefined;
            const fields = base.getFields();
            return Object.values(fields).map((field) => ({
              name: field.name,
              type: String(field.type),
              description: field.description || undefined,
            }));
          };

          let nextInfo: CursorInfo | null = null;

          if (context?.typeInfo) {
            const { typeInfo, token } = context;
            const { kind, step } = token.state;

            if (
              (kind === 'Field' && step === 0 && typeInfo.fieldDef) ||
              (kind === 'AliasedField' && step === 2 && typeInfo.fieldDef) ||
              (kind === 'ObjectField' && step === 0 && typeInfo.fieldDef)
            ) {
              const field = typeInfo.fieldDef;
              const parent = typeInfo.parentType ? String(typeInfo.parentType) : 'Query';
              nextInfo = {
                kind: 'field',
                signature: `${parent}.${field.name}: ${String(field.type)}`,
                description: field.description || undefined,
                args: field.args?.map((arg) => ({
                  name: arg.name,
                  type: String(arg.type),
                  description: arg.description || undefined,
                  inputFields: getInputFields(arg.type),
                })),
              };
            } else if (kind === 'Argument' && step === 0 && typeInfo.argDef) {
              const arg = typeInfo.argDef;
              nextInfo = {
                kind: 'argument',
                signature: `${arg.name}: ${String(arg.type)}`,
                description: arg.description || undefined,
                inputFields: getInputFields(arg.type),
              };
            } else if (
              kind === 'EnumValue' &&
              typeInfo.enumValue &&
              'description' in typeInfo.enumValue
            ) {
              const enumType = typeInfo.inputType ? String(typeInfo.inputType) : 'Enum';
              nextInfo = {
                kind: 'enum',
                signature: `${enumType}.${typeInfo.enumValue.name}`,
                description: typeInfo.enumValue.description || undefined,
              };
            } else if (kind === 'Variable' && typeInfo.type) {
              nextInfo = {
                kind: 'variable',
                signature: String(typeInfo.type),
                description: (typeInfo.type as unknown as { description?: string }).description || undefined,
              };
            } else if (kind === 'Directive' && step === 1 && typeInfo.directiveDef) {
              nextInfo = {
                kind: 'directive',
                signature: `@${typeInfo.directiveDef.name}`,
                description: typeInfo.directiveDef.description || undefined,
              };
            } else if (kind === 'NamedType' && typeInfo.type) {
              const base = unwrapInputType(typeInfo.type);
              nextInfo = {
                kind: 'type',
                signature: String(typeInfo.type),
                description: (typeInfo.type as unknown as { description?: string }).description || undefined,
                inputFields: base instanceof GraphQLInputObjectType
                  ? Object.values(base.getFields()).map((field) => ({
                      name: field.name,
                      type: String(field.type),
                      description: field.description || undefined,
                    }))
                  : undefined,
                enumValues: base instanceof GraphQLEnumType
                  ? base.getValues().map((value) => ({
                      name: value.name,
                      description: value.description || undefined,
                    }))
                  : undefined,
              };
            }
          }

          const signature = nextInfo?.signature || null;
          if (signature !== lastCursorSignature) {
            lastCursorSignature = signature;
            onCursorInfo(nextInfo);
          }
        }
      }),

      // Keyboard event handler
      EditorView.domEventHandlers({
        keydown: (event) => {
          onkeydown?.(event);
          // Don't prevent default - let CodeMirror handle it
          return false;
        },
      }),

      // Min height
      EditorView.theme({
        '&': {
          minHeight,
        },
        '.cm-scroller': {
          minHeight,
        },
      }),
    ].filter(Boolean);

    // Create editor
    view = new EditorView({
      state: EditorState.create({
        doc: value,
        extensions: extensions as import('@codemirror/state').Extension[],
      }),
      parent: container,
    });

    isInitialized = true;
  }

  // Update editor when value changes externally
  $effect(() => {
    const currentValue = value;

    if (view && isInitialized) {
      const editorContent = view.state.doc.toString();
      if (currentValue !== editorContent) {
        isExternalUpdate = true;
        view.dispatch({
          changes: {
            from: 0,
            to: view.state.doc.length,
            insert: currentValue,
          },
        });
        isExternalUpdate = false;
      }
    }
  });

  // Update schema when it changes (enables autocomplete)
  $effect(() => {
    const currentSchema = schema;

    if (view && isInitialized && updateSchemaFn && language === 'graphql') {
      const nextSchema = buildSchema(currentSchema);
      if (nextSchema) {
        // Keep the shared schema reference updated for hover + cursor docs.
        graphqlSchema = nextSchema;
        updateSchemaFn(view, nextSchema);
      }
    }
  });
</script>

<div
  bind:this={container}
  class="codemirror-container {className}"
  class:readonly
></div>

<style>
  .codemirror-container {
    width: 100%;
    height: 100%;
    overflow: hidden;
  }

  .codemirror-container :global(.cm-editor) {
    height: 100%;
    max-height: 100%;
    border: none;
    outline: none;
  }

  .codemirror-container :global(.cm-scroller) {
    overflow: auto !important;
  }

  .codemirror-container :global(.cm-editor.cm-focused) {
    outline: none;
  }

  .codemirror-container.readonly :global(.cm-content) {
    cursor: default;
  }

  /* Hide cursor in readonly mode */
  .codemirror-container.readonly :global(.cm-cursor) {
    display: none !important;
  }

  /* GraphQL Hover Tooltips */
  .codemirror-container :global(.cm-tooltip-hover) {
    background: hsl(var(--popover));
    border: 1px solid hsl(var(--border));
    border-radius: 4px;
    padding: 0;
    max-width: 400px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
  }

  .codemirror-container :global(.cm-graphql-hover) {
    padding: 8px 12px;
    font-size: 12px;
    line-height: 1.4;
  }

  .codemirror-container :global(.cm-hover-code) {
    background: hsl(var(--muted));
    padding: 6px 8px;
    margin: 4px 0;
    border-radius: 3px;
    font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace;
    font-size: 11px;
    color: hsl(var(--primary));
    white-space: pre-wrap;
    overflow-x: auto;
  }

  .codemirror-container :global(.cm-hover-text) {
    color: hsl(var(--muted-foreground));
    margin: 4px 0;
  }

  .codemirror-container :global(.cm-hover-text:first-child) {
    margin-top: 0;
  }

  /* Lint Diagnostics */
  .codemirror-container :global(.cm-lintRange-error) {
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='6' height='3'%3E%3Cpath d='m0 3 l2 -2 l1 0 l2 2 l1 0' stroke='%23f87171' fill='none' stroke-width='.7'%3E%3C/path%3E%3C/svg%3E");
    background-repeat: repeat-x;
    background-position: left bottom;
    padding-bottom: 2px;
  }

  .codemirror-container :global(.cm-lintRange-warning) {
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='6' height='3'%3E%3Cpath d='m0 3 l2 -2 l1 0 l2 2 l1 0' stroke='%23fbbf24' fill='none' stroke-width='.7'%3E%3C/path%3E%3C/svg%3E");
    background-repeat: repeat-x;
    background-position: left bottom;
    padding-bottom: 2px;
  }

  /* Lint Gutter */
  .codemirror-container :global(.cm-lint-marker-error) {
    content: '';
    width: 8px;
    height: 8px;
    background: #f87171;
    border-radius: 50%;
  }

  .codemirror-container :global(.cm-lint-marker-warning) {
    content: '';
    width: 8px;
    height: 8px;
    background: #fbbf24;
    border-radius: 50%;
  }

  /* Lint Tooltip */
  .codemirror-container :global(.cm-tooltip-lint) {
    background: hsl(var(--popover));
    border: 1px solid hsl(var(--border));
    border-radius: 4px;
    padding: 6px 10px;
    font-size: 12px;
    max-width: 350px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
  }

  .codemirror-container :global(.cm-diagnostic-error) {
    border-left: 3px solid #f87171;
    padding-left: 8px;
    color: hsl(var(--foreground));
  }

  .codemirror-container :global(.cm-diagnostic-warning) {
    border-left: 3px solid #fbbf24;
    padding-left: 8px;
    color: hsl(var(--foreground));
  }

  /* Lint panel */
  .codemirror-container :global(.cm-panel.cm-panel-lint) {
    background: hsl(var(--muted));
    border-top: 1px solid hsl(var(--border));
  }

  .codemirror-container :global(.cm-panel.cm-panel-lint ul) {
    padding: 4px;
    margin: 0;
    list-style: none;
  }

  .codemirror-container :global(.cm-panel.cm-panel-lint li) {
    padding: 4px 8px;
    cursor: pointer;
    font-size: 12px;
  }

  .codemirror-container :global(.cm-panel.cm-panel-lint li:hover) {
    background: hsl(var(--accent));
  }
</style>
