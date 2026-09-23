/**
 * The result type of a selection, projected from what the document built for
 * it returns rather than from the whole schema type.
 *
 * Genql's own FieldsSelection returns every field of a union member whatever
 * its on_<Type> branch selects, and types `__scalar: true` as every scalar
 * field, including those the runtime leaves out because they need an
 * argument. This type follows the runtime instead:
 *
 * - An object level returns exactly the fields it selects, each projected
 *   through its own selection. A field set to false is left out.
 * - `__scalar: true` adds the fields generated/select/selectionTypes.ts lists
 *   for the type, which are the ones linkTypeMap puts in the fragment.
 * - A union or interface level returns, for each member type, `__typename`
 *   (select.ts always selects it there), the fields selected on the level
 *   itself, and the fields of every on_<Type> branch that member matches: its
 *   own type, or an interface it implements.
 *
 * It depends on Genql's schema.ts only for the shapes FieldsSelection read
 * too: each object's `__typename` literal and the `__isUnion` marker on
 * unions and interfaces.
 */
import type { PossibleTypes, scalarFields } from "./generated/select/selectionTypes.js";

type Nil = null | undefined;
type Falsy = false | 0 | Nil;
type Scalar = string | number | boolean;
type Keyword = "__args" | "__name" | "__scalar";

/** The result of selection DST on response type SRC. */
export type FieldsSelection<SRC, DST> = DST extends Falsy ? never : Project<SRC, DST>;

type Project<SRC, DST> = unknown extends SRC
  ? SRC
  : ProjectValue<NonNullable<SRC>, DST> | Extract<SRC, Nil>;

type ProjectValue<S, DST> = [S] extends [never]
  ? never
  : [S] extends [Scalar]
    ? S
    : [S] extends [readonly (infer T)[]]
      ? Project<T, DST>[]
      : DST extends boolean | number
        ? S
        : "__isUnion" extends keyof S
          ? ProjectAbstract<S, DST, CommonScalarFields<AbstractNames<S>>>
          : Simplify<ProjectFields<S, DST, ScalarFieldsOf<TypeName<S>>>>;

type Simplify<T> = { [K in keyof T]: T[K] } & {};

type TypeName<S> = S extends { __typename: infer N } ? N : never;

type ScalarFieldsOf<N> = N extends keyof typeof scalarFields
  ? (typeof scalarFields)[N][number]
  : never;

type PossibleTypesOf<N> = N extends keyof PossibleTypes ? PossibleTypes[N] : N;

// A union or interface response type is the union of its members, so its name
// is recovered as the abstract type with exactly those members. A union and
// an interface with the same members both match; their scalar fields are then
// intersected, which never claims a field one of them lacks.
type AbstractNames<S> = {
  [N in keyof PossibleTypes]: [PossibleTypes[N]] extends [TypeName<S>]
    ? [TypeName<S>] extends [PossibleTypes[N]]
      ? N
      : never
    : never;
}[keyof PossibleTypes];

type CommonScalarFields<N> = [N] extends [never]
  ? never
  : (N extends unknown ? (fields: ScalarFieldsOf<N>) => void : never) extends (
        fields: infer F
      ) => void
    ? F
    : never;

type UnionToIntersection<U> = (U extends unknown ? (value: U) => void : never) extends (
  value: infer I
) => void
  ? I
  : never;

type FalsyKeys<DST> = {
  [K in keyof DST]-?: NonNullable<DST[K]> extends Falsy ? K : never;
}[keyof DST];

type SelectedKeys<DST> = Exclude<
  { [K in keyof DST]-?: K extends Keyword | `on_${string}` ? never : K }[keyof DST],
  FalsyKeys<DST>
>;

type ScalarKeys<DST, Scalars> = "__scalar" extends keyof DST
  ? NonNullable<DST["__scalar" & keyof DST]> extends Falsy
    ? never
    : Exclude<Scalars, FalsyKeys<DST>>
  : never;

type ProjectFields<S, DST, Scalars> = {
  [K in keyof S as K extends SelectedKeys<DST> | ScalarKeys<DST, Scalars>
    ? K
    : never]: K extends SelectedKeys<DST> ? FieldsSelection<S[K], DST[K & keyof DST]> : S[K];
};

type ProjectAbstract<S, DST, Scalars> = S extends unknown
  ? Simplify<ProjectMember<S, DST, Scalars>>
  : never;

type ProjectMember<M, DST, Scalars> = { __typename: TypeName<M> } & ProjectFields<M, DST, Scalars> &
  UnionToIntersection<Branches<M, DST>>;

// Each on_<Type> branch that applies to member M; a branch can be an
// interface selection with branches of its own.
type Branches<M, DST> = {
  [K in keyof DST]-?: K extends `on_${infer N}`
    ? NonNullable<DST[K]> extends Falsy
      ? never
      : TypeName<M> extends PossibleTypesOf<N>
        ? ProjectMember<M, NonNullable<DST[K]>, ScalarFieldsOf<N>>
        : never
    : never;
}[keyof DST];
