// Package generate provides a function to generate Go source code for LSP types.
package generate

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/marcuscaisey/lox/loxls/lsp/protocol/typegen/metamodel"
)

// Source returns an unformatted Go source file containing declarations of the given types.
// Types are resolved using the given meta model.
// The file will belong to the given package.
func Source(types []*metamodel.Type, metaModel *metamodel.MetaModel, pkg string) string {
	generator := newGenerator(types, metaModel, pkg)
	return generator.Source()
}

type generator struct {
	types     []*metamodel.Type
	metaModel *metamodel.MetaModel
	pkg       string

	typeDecls    []string
	importedPkgs map[string]struct{}
	gennedTypes  map[string]bool
}

func newGenerator(types []*metamodel.Type, metaModel *metamodel.MetaModel, pkg string) *generator {
	g := &generator{
		types:        types,
		metaModel:    metaModel,
		pkg:          pkg,
		importedPkgs: map[string]struct{}{},
		gennedTypes:  map[string]bool{},
	}
	return g
}

func (g *generator) Source() string {
	for _, typ := range g.types {
		if isNullBaseType(typ) {
			continue
		}
		namespace := ""
		g.genTypeDecl(namespace, typ)
	}

	const text = `
// Code generated by "typegen {{.args}}"; DO NOT EDIT.
package {{.package}}

{{if .importedPackages}}
import (
{{range .importedPackages}}
	"{{.}}"
{{- end}}
)
{{end}}

{{range .typeDeclarations}}
{{.}}
{{end}}
`
	data := map[string]any{
		"args":             strings.Join(os.Args[1:], " "),
		"package":          g.pkg,
		"importedPackages": slices.Collect(maps.Keys(g.importedPkgs)),
		"typeDeclarations": g.typeDecls,
	}
	return mustExecuteTemplate(text, data)
}

func (g *generator) genTypeDecl(namespace string, typ *metamodel.Type) string {
	switch typ := typ.Value.(type) {
	case metamodel.ReferenceType:
		return g.genRefTypeDecl(typ.Name)
	case metamodel.OrType:
		return g.genSumTypeDecl(namespace, typ.Items)
	case metamodel.BaseType:
		return g.genBaseTypeDecl(typ.Name)
	case metamodel.StructureLiteralType:
		return g.genStructDeclForLiteral(namespace, typ.Value)
	case metamodel.ArrayType:
		return g.genSliceDecl(namespace, typ.Element)
	case metamodel.MapType:
		return g.genMapDecl(namespace, typ.Key, typ.Value)
	default:
		panic(fmt.Sprintf("unhandled type: %T", typ))
	}
}

func (g *generator) genRefTypeDecl(name string) string {
	if structure, ok := g.metaModel.Structure(name); ok {
		return g.genStructDecl(structure)
	} else if alias, ok := g.metaModel.TypeAlias(name); ok {
		return g.genTypeAliasDecl(alias)
	} else if enum, ok := g.metaModel.Enumeration(name); ok {
		return g.genEnumDecl(enum)
	} else {
		panic(fmt.Sprintf("invalid reference type: %s", name))
	}
}

func (g *generator) genStructDecl(structure *metamodel.Structure) string {
	name := structure.Name

	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true

	comment := g.commentForType(name, structure.Documentation, structure.Deprecated)
	var fields []string
	for _, typ := range slices.Concat(structure.Extends, structure.Mixins) {
		typ, ok := typ.Value.(metamodel.ReferenceType)
		if !ok {
			panic("non-reference parent type or mixin not supported")
		}
		fields = append(fields, g.genRefTypeDecl(typ.Name))
	}
	for _, prop := range structure.Properties {
		fields = append(fields, g.structField(name, prop))
	}

	const text = `
{{.comment}}
type {{.name}} struct {
	{{- range .fields}}
	{{.}}
	{{- end}}
}
`
	data := map[string]any{"comment": comment, "name": name, "fields": fields}
	decl := mustExecuteTemplate(text, data)
	g.typeDecls = append(g.typeDecls, decl)

	return name
}

func (g *generator) structField(namespace string, prop *metamodel.Property) string {
	const text = `
{{- .comment}}
{{- if .optional}}
{{.fieldName}} *{{.type}} {{jsonTag .jsonName "omitempty"}}
{{- else}}
{{.fieldName}} {{.type}} {{jsonTag .jsonName}}
{{- end -}}
`
	fieldName := upperFirstLetter(prop.Name)
	data := map[string]any{
		"comment":   g.comment(prop.Documentation, prop.Deprecated),
		"optional":  prop.Optional != nil && *prop.Optional,
		"fieldName": fieldName,
		"type":      g.genTypeDecl(namespace+fieldName, prop.Type),
		"jsonName":  prop.Name,
	}
	return mustExecuteTemplate(text, data)
}

func (g *generator) genTypeAliasDecl(typeAlias *metamodel.TypeAlias) string {
	name := typeAlias.Name

	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true

	const text = `
{{.comment}}
type {{.name}} = {{.type}}
`
	data := map[string]any{
		"name":    name,
		"comment": g.commentForType(name, typeAlias.Documentation, typeAlias.Deprecated),
		"type":    g.genTypeDecl(name, typeAlias.Type),
	}
	decl := mustExecuteTemplate(text, data)
	g.typeDecls = append(g.typeDecls, decl)

	return name
}

var enumTypeTypes = map[metamodel.EnumerationTypeName]string{
	metamodel.EnumerationTypeNameString:   "string",
	metamodel.EnumerationTypeNameInteger:  "int32",
	metamodel.EnumerationTypeNameUinteger: "uint32",
}

func (g *generator) genEnumDecl(enum *metamodel.Enumeration) string {
	name := enum.Name

	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true

	comment := g.commentForType(name, enum.Documentation, enum.Deprecated)

	typ, ok := enumTypeTypes[enum.Type.Name]
	if !ok {
		panic(fmt.Sprintf("unhandled enumeration type: %s", enum.Type.Name))
	}

	type enumMember struct {
		Comment, Name, Value string
	}
	members := make([]enumMember, len(enum.Values))
	for i, entry := range enum.Values {
		var value string
		switch entry := entry.Value.Value.(type) {
		case metamodel.Int:
			value = fmt.Sprint(entry)
		case metamodel.String:
			value = fmt.Sprintf("%q", entry)
		}
		members[i] = enumMember{
			Comment: g.comment(entry.Documentation, entry.Deprecated),
			Name:    fmt.Sprintf("%s%s", name, entry.Name),
			Value:   value,
		}
	}

	const text = `
{{.comment}}
type {{.name}} {{.type}}

const (
	{{range .members}}
	{{- .Comment}}
	{{.Name}} {{$.name}} = {{.Value}}
	{{- end}}
)

{{if not .supportsCustomValues}}
{{with $validValuesVar := printf "valid%sValues" .name}}
var {{$validValuesVar}} = map[{{$.type}}]bool{
	{{- range $.members}}
	{{.Value}}: true,
	{{- end}}
}

{{with $receiver := slice $.name 0 1 | lowerFirstLetter}}
func ({{$receiver}} *{{$.name}}) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	{{- with $valueVar := lowerFirstLetter $.type | printf "%sValue"}}
	var {{$valueVar}} {{$.type}}
	if err := json.Unmarshal(data, &{{$valueVar}}); err != nil {
		return err
	}
	if !{{$validValuesVar}}[{{$valueVar}}] {
		return fmt.Errorf("cannot unmarshal %v into {{$.name}}: custom values are not supported", {{$valueVar}})
	}
	*{{$receiver}} = {{$.name}}({{$valueVar}})
	{{end}}
	return nil
}

func ({{$receiver}} {{$.name}}) MarshalJSON() ([]byte, error) {
	{{- with $valueVar := lowerFirstLetter $.type | printf "%sValue"}}
	var {{$valueVar}} = {{$.type}}({{$receiver}})
	if !{{$validValuesVar}}[{{$valueVar}}] {
		return nil, fmt.Errorf("cannot marshal %v into {{$.name}}: custom values are not supported", {{$valueVar}})
	}
	return json.Marshal({{$valueVar}})
	{{end}}
}
{{end}}
{{end}}
{{end}}
`
	g.importPkgs("fmt")
	data := map[string]any{"comment": comment, "name": name, "type": typ, "members": members, "supportsCustomValues": enum.SupportsCustomValues}
	decl := mustExecuteTemplate(text, data)
	g.typeDecls = append(g.typeDecls, decl)

	return name
}

func (g *generator) genSumTypeDecl(namespace string, variants []*metamodel.Type) (name string) {
	nonNullVariants := slices.DeleteFunc(slices.Clone(variants), isNullBaseType)
	defer func() {
		if nullable := len(nonNullVariants) != len(variants); nullable {
			name = fmt.Sprintf("*%s", name)
		}
	}()

	variantTypes := make([]string, len(nonNullVariants))
	for i, item := range nonNullVariants {
		variantTypes[i] = g.genTypeDecl(fmt.Sprintf("%sOr%d", namespace, i), item)
	}
	if len(variantTypes) == 1 {
		return variantTypes[0]
	}

	slices.Sort(variantTypes)
	name = strings.Join(variantTypes, "Or")

	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true

	const text = `
{{with $interface := .name | printf "%sValue" -}}
// {{$.name}} contains either of the following types:
{{- range $.variants}}
//   - [{{.}}]
{{- end}}
type {{$.name}} struct {
	Value {{$interface}}
}

{{with $interfaceMethod := $interface | printf "is%s" -}}
// {{$interface}} is either of the following types:{{- range $.variants}}
//   - [{{.}}]
{{- end}}
type {{$interface}} interface {
	{{$interfaceMethod}}()
}
{{range $.variants}}
func ({{.}}) {{$interfaceMethod}}() {}
{{- end}}
{{- end}}
{{- end}}

{{with $receiver := slice .name 0 1 | lowerFirstLetter}}
func ({{$receiver}} *{{$.name}}) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	{{- range $i, $variant := $.variants}}
	{{- with $var := lowerFirstLetter $variant | printf "%sValue"}}
	var {{$var}} {{$variant}}
	if err := json.Unmarshal(data, &{{$var}}); err == nil {
		{{$receiver}}.Value = {{$var}}
		return nil
	}
	{{- end}}
	{{- end}}
	return &json.UnmarshalTypeError{
		Value: string(data),
		Type:  reflect.TypeFor[*{{$.name}}](),
	}
}

func ({{$receiver}} {{$.name}}) MarshalJSON() ([]byte, error) {
	return json.Marshal({{$receiver}}.Value)
}
{{end}}
`
	g.importPkgs("bytes", "encoding/json", "reflect")
	data := map[string]any{"name": name, "variants": variantTypes}
	decl := mustExecuteTemplate(text, data)
	g.typeDecls = append(g.typeDecls, decl)

	return name
}

var baseTypeTypes = map[metamodel.BaseTypes]string{
	metamodel.BaseTypesURI:         "string",
	metamodel.BaseTypesDocumentURI: "string",
	metamodel.BaseTypesInteger:     "int32",
	metamodel.BaseTypesUinteger:    "uint32",
	metamodel.BaseTypesDecimal:     "float64",
	metamodel.BaseTypesRegExp:      "*regexp.Regexp",
	metamodel.BaseTypesString:      "string",
	metamodel.BaseTypesBoolean:     "bool",
}

func (g *generator) genBaseTypeDecl(baseType metamodel.BaseTypes) string {
	typ, ok := baseTypeTypes[baseType]
	if !ok {
		panic(fmt.Sprintf("unhandled base type: %s", baseType))
	}
	name := upperFirstLetter(string(baseType))
	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true
	decl := fmt.Sprintf("type %s %s", name, typ)
	g.typeDecls = append(g.typeDecls, decl)
	return name
}

func (g *generator) genStructDeclForLiteral(namespace string, structLiteral metamodel.StructureLiteral) string {
	name := namespace

	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true

	comment := g.comment(structLiteral.Documentation, structLiteral.Deprecated)
	var fields []string
	for _, prop := range structLiteral.Properties {
		fields = append(fields, g.structField(name, prop))
	}

	const text = `
{{.comment}}
type {{.name}} struct {
	{{- range .fields}}
	{{.}}
	{{- end}}
}
`
	data := map[string]any{"comment": comment, "name": name, "fields": fields}
	decl := mustExecuteTemplate(text, data)
	g.typeDecls = append(g.typeDecls, decl)

	return name
}

func (g *generator) genSliceDecl(namespace string, elementType *metamodel.Type) string {
	goElementType := g.genTypeDecl(namespace, elementType)
	name := fmt.Sprintf("%sSlice", goElementType)
	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true
	g.typeDecls = append(g.typeDecls, fmt.Sprintf("type %s []%s", name, goElementType))
	return name
}

func (g *generator) genMapDecl(namespace string, keyType metamodel.MapKeyType, valueType *metamodel.Type) string {
	var goKeyType string
	switch key := keyType.Value.(type) {
	case metamodel.BaseMapKeyType:
		goKeyType = g.genBaseTypeDecl(metamodel.BaseTypes(key.Name))
	case metamodel.ReferenceType:
		goKeyType = g.genRefTypeDecl(key.Name)
	}
	goValueType := g.genTypeDecl(namespace, valueType)
	name := fmt.Sprintf("%s%sMap", goKeyType, goValueType)
	if g.gennedTypes[name] {
		return name
	}
	g.gennedTypes[name] = true
	decl := fmt.Sprintf("type %s map[%s]%s", name, goKeyType, goValueType)
	g.typeDecls = append(g.typeDecls, decl)
	return name
}

func (g *generator) importPkgs(pkgs ...string) {
	for _, pkg := range pkgs {
		g.importedPkgs[pkg] = struct{}{}
	}
}

func (g *generator) comment(documentation, deprecationMsg *string) string {
	comment := ""
	if documentation != nil {
		comment = *documentation
	}
	if deprecationMsg != nil {
		if strings.Contains(comment, "@deprecated") {
			comment = strings.ReplaceAll(comment, "@deprecated", "Deprecated:")
		} else {
			if comment != "" {
				comment += "\n\n"
			}
			comment += "Deprecated: " + *deprecationMsg
		}
	}
	if comment != "" {
		return "// " + strings.ReplaceAll(comment, "\n", "\n// ")
	} else {
		return ""
	}
}

func (g *generator) commentForType(name string, documentation, deprecationMsg *string) string {
	comment := g.comment(documentation, deprecationMsg)
	versionParts := strings.Split(g.metaModel.MetaData.Version, ".")
	major, minor := versionParts[0], versionParts[1]
	if comment != "" {
		comment += "\n//\n"
	}
	comment += fmt.Sprintf("// https://microsoft.github.io/language-server-protocol/specifications/lsp/%s.%s/specification/#%s", major, minor, lowerFirstLetter(name))
	return comment
}

func mustExecuteTemplate(text string, data map[string]any) string {
	funcMap := template.FuncMap{
		"jsonTag":          jsonTag,
		"lowerFirstLetter": lowerFirstLetter,
	}
	tmpl := template.Must(template.New("template").Funcs(funcMap).Parse(text))
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		panic(err)
	}
	return b.String()
}

func lowerFirstLetter(s string) string {
	return strings.ToLower(s[0:1]) + s[1:]
}

func upperFirstLetter(s string) string {
	return strings.ToUpper(s[0:1]) + s[1:]
}

func jsonTag(name string, opts ...string) string {
	return fmt.Sprintf("`json:%q`", strings.Join(append([]string{name}, opts...), ","))
}

func isNullBaseType(typ *metamodel.Type) bool {
	baseType, ok := typ.Value.(metamodel.BaseType)
	return ok && baseType.Name == metamodel.BaseTypesNull
}
