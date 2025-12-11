<script lang="ts">
  import { onMount } from 'svelte';
  import { browser } from '$app/environment';
  import type { GraphQLSchema } from 'graphql';

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
  }: Props = $props();

  let container: HTMLDivElement;
  let view: import('@codemirror/view').EditorView | null = null;
  let isInitialized = $state(false);

  // Track if we're updating from external value change
  let isExternalUpdate = false;

  // Store function reference for updateSchema
  let updateSchemaFn: ((view: import('@codemirror/view').EditorView, schema: GraphQLSchema) => void) | null = null;
  let buildClientSchemaFn: typeof import('graphql').buildClientSchema | null = null;

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
    const { closeBrackets, closeBracketsKeymap, autocompletion } = await import('@codemirror/autocomplete');
    const { lineNumbers, highlightActiveLineGutter, highlightActiveLine } = await import('@codemirror/view');
    const { json } = await import('@codemirror/lang-json');
    const { graphql, updateSchema } = await import('cm6-graphql');
    const { tokyoNight } = await import('./codemirrorTheme');
    const { buildClientSchema } = await import('graphql');

    // Store function references for later use
    updateSchemaFn = updateSchema;
    buildClientSchemaFn = buildClientSchema;

    // Build initial GraphQL schema if available
    const graphqlSchema = buildSchema(schema);

    // Build extensions array based on language
    const languageExtension = language === 'graphql'
      ? graphql(graphqlSchema)
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
        ...defaultKeymap,
        ...historyKeymap,
        ...closeBracketsKeymap,
        ...foldKeymap,
      ]),

      // Placeholder text
      placeholder && placeholderExtension(placeholder),

      // Language support
      languageExtension,

      // Read-only mode
      readonly && EditorState.readOnly.of(true),

      // Update listener
      EditorView.updateListener.of((update) => {
        if (update.docChanged && !isExternalUpdate) {
          const newValue = update.state.doc.toString();
          value = newValue;
          onchange?.(newValue);
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
      const graphqlSchema = buildSchema(currentSchema);
      if (graphqlSchema) {
        updateSchemaFn(view, graphqlSchema);
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
    overflow: hidden;
  }

  .codemirror-container :global(.cm-editor) {
    height: 100%;
    border: none;
    outline: none;
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
</style>
