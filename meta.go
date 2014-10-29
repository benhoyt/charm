// Copyright 2011, 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package charm

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"strconv"
	"strings"

	"github.com/juju/schema"
	"gopkg.in/yaml.v1"

	"gopkg.in/juju/charm.v4/hooks"
)

// RelationScope describes the scope of a relation.
type RelationScope string

// Note that schema doesn't support custom string types,
// so when we use these values in a schema.Checker,
// we must store them as strings, not RelationScopes.

const (
	ScopeGlobal    RelationScope = "global"
	ScopeContainer RelationScope = "container"
)

// RelationRole defines the role of a relation.
type RelationRole string

const (
	RoleProvider RelationRole = "provider"
	RoleRequirer RelationRole = "requirer"
	RolePeer     RelationRole = "peer"
)

// StorageType defines a storage type.
type StorageType string

const (
	StorageBlock      StorageType = "block"
	StorageFilesystem StorageType = "filesystem"
)

// Storage represents a charm's storage requirement.
type Storage struct {
	// Name is the name of the storage requirement.
	Name string

	// Type is the storage type: filesystem or block-device.
	Type StorageType

	// Shared indicates that the storage is shared between all units of
	// a service deployed from the charm. It is an error to attempt to
	// assign non-shareable storage to a "shared" storage requirement.
	Shared bool

	// ReadOnly indicates that the storage should be made read-only if
	// possible. If the storage cannot be made read-only, Juju will warn
	// the user.
	ReadOnly bool

	// Persistent indicates that the storage should be made persistent
	// if possible. If the storage cannot be made persistent, Juju will
	// warn the user.
	Persistent bool

	// CountMin is the number of storage instances that must be attached
	// to the charm for it to be useful; the charm will not install until
	// this number has been satisfied. This must be a non-negative number.
	CountMin int

	// CountMax is the largest number of storage instances that can be
	// attached to the charm. If CountMax is -1, then there is no upper
	// bound.
	CountMax int

	// Location is the mount location for filesystem stores. If count does
	// not have a maximum of 1, then location acts as the parent directory
	// for each mounted store.
	Location string `bson:",omitempty"`

	// Filesystem is the list of filesystems that Juju will attempt to
	// create, in order of most to least preferred.
	Filesystem []Filesystem `bson:",omitempty"`
}

type Filesystem struct {
	// Type is the filesystem type.
	Type string

	// MkfsOptions is any options to use when creating the filesystem.
	// MkfsOptions will be passed directly to "mkfs".
	MkfsOptions []string `bson:",omitempty"`

	// MountOptions is any options to use when mounting the filesystem.
	// MountOptions will be passed directly to "mount".
	MountOptions []string `bson:",omitempty"`
}

// Relation represents a single relation defined in the charm
// metadata.yaml file.
type Relation struct {
	Name      string
	Role      RelationRole
	Interface string
	Optional  bool
	Limit     int
	Scope     RelationScope
}

// ImplementedBy returns whether the relation is implemented by the supplied charm.
func (r Relation) ImplementedBy(ch Charm) bool {
	if r.IsImplicit() {
		return true
	}
	var m map[string]Relation
	switch r.Role {
	case RoleProvider:
		m = ch.Meta().Provides
	case RoleRequirer:
		m = ch.Meta().Requires
	case RolePeer:
		m = ch.Meta().Peers
	default:
		panic(fmt.Errorf("unknown relation role %q", r.Role))
	}
	rel, found := m[r.Name]
	if !found {
		return false
	}
	if rel.Interface == r.Interface {
		switch r.Scope {
		case ScopeGlobal:
			return rel.Scope != ScopeContainer
		case ScopeContainer:
			return true
		default:
			panic(fmt.Errorf("unknown relation scope %q", r.Scope))
		}
	}
	return false
}

// IsImplicit returns whether the relation is supplied by juju itself,
// rather than by a charm.
func (r Relation) IsImplicit() bool {
	return (r.Name == "juju-info" &&
		r.Interface == "juju-info" &&
		r.Role == RoleProvider)
}

// Meta represents all the known content that may be defined
// within a charm's metadata.yaml file.
type Meta struct {
	Name        string
	Summary     string
	Description string
	Subordinate bool
	Provides    map[string]Relation `bson:",omitempty"`
	Requires    map[string]Relation `bson:",omitempty"`
	Peers       map[string]Relation `bson:",omitempty"`
	Format      int                 `bson:",omitempty"`
	OldRevision int                 `bson:",omitempty"` // Obsolete
	Categories  []string            `bson:",omitempty"`
	Tags        []string            `bson:",omitempty"`
	Series      string              `bson:",omitempty"`
	Storage     map[string]Storage  `bson:",omitempty"`
}

func generateRelationHooks(relName string, allHooks map[string]bool) {
	for _, hookName := range hooks.RelationHooks() {
		allHooks[fmt.Sprintf("%s-%s", relName, hookName)] = true
	}
}

// Hooks returns a map of all possible valid hooks, taking relations
// into account. It's a map to enable fast lookups, and the value is
// always true.
func (m Meta) Hooks() map[string]bool {
	allHooks := make(map[string]bool)
	// Unit hooks
	for _, hookName := range hooks.UnitHooks() {
		allHooks[string(hookName)] = true
	}
	// Relation hooks
	for hookName := range m.Provides {
		generateRelationHooks(hookName, allHooks)
	}
	for hookName := range m.Requires {
		generateRelationHooks(hookName, allHooks)
	}
	for hookName := range m.Peers {
		generateRelationHooks(hookName, allHooks)
	}
	return allHooks
}

// Used for parsing Categories and Tags.
func parseStringList(list interface{}) []string {
	if list == nil {
		return nil
	}
	slice := list.([]interface{})
	result := make([]string, 0, len(slice))
	for _, elem := range slice {
		result = append(result, elem.(string))
	}
	return result
}

// ReadMeta reads the content of a metadata.yaml file and returns
// its representation.
func ReadMeta(r io.Reader) (meta *Meta, err error) {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return
	}
	raw := make(map[interface{}]interface{})
	err = yaml.Unmarshal(data, raw)
	if err != nil {
		return
	}
	v, err := charmSchema.Coerce(raw, nil)
	if err != nil {
		return nil, errors.New("metadata: " + err.Error())
	}
	m := v.(map[string]interface{})
	meta = &Meta{}
	meta.Name = m["name"].(string)
	// Schema decodes as int64, but the int range should be good
	// enough for revisions.
	meta.Summary = m["summary"].(string)
	meta.Description = m["description"].(string)
	meta.Provides = parseRelations(m["provides"], RoleProvider)
	meta.Requires = parseRelations(m["requires"], RoleRequirer)
	meta.Peers = parseRelations(m["peers"], RolePeer)
	meta.Format = int(m["format"].(int64))
	meta.Categories = parseStringList(m["categories"])
	meta.Tags = parseStringList(m["tags"])
	if subordinate := m["subordinate"]; subordinate != nil {
		meta.Subordinate = subordinate.(bool)
	}
	if rev := m["revision"]; rev != nil {
		// Obsolete
		meta.OldRevision = int(m["revision"].(int64))
	}
	if series, ok := m["series"]; ok && series != nil {
		meta.Series = series.(string)
	}
	meta.Storage = parseStorage(m["storage"])
	if err := meta.Check(); err != nil {
		return nil, err
	}
	return meta, nil
}

// GetYAML implements yaml.Getter.GetYAML.
func (m Meta) GetYAML() (tag string, value interface{}) {
	marshaledRelations := func(rs map[string]Relation) map[string]marshaledRelation {
		mrs := make(map[string]marshaledRelation)
		for name, r := range rs {
			mrs[name] = marshaledRelation(r)
		}
		return mrs
	}
	return "", struct {
		Name        string                       `yaml:"name"`
		Summary     string                       `yaml:"summary"`
		Description string                       `yaml:"description"`
		Provides    map[string]marshaledRelation `yaml:"provides,omitempty"`
		Requires    map[string]marshaledRelation `yaml:"requires,omitempty"`
		Peers       map[string]marshaledRelation `yaml:"peers,omitempty"`
		Categories  []string                     `yaml:"categories,omitempty"`
		Tags        []string                     `yaml:"tags,omitempty"`
		Subordinate bool                         `yaml:"subordinate,omitempty"`
		Series      string                       `yaml:"series,omitempty"`
	}{
		Name:        m.Name,
		Summary:     m.Summary,
		Description: m.Description,
		Provides:    marshaledRelations(m.Provides),
		Requires:    marshaledRelations(m.Requires),
		Peers:       marshaledRelations(m.Peers),
		Categories:  m.Categories,
		Tags:        m.Tags,
		Subordinate: m.Subordinate,
		Series:      m.Series,
	}
}

type marshaledRelation Relation

func (r marshaledRelation) GetYAML() (tag string, value interface{}) {
	// See calls to ifaceExpander in charmSchema.
	noLimit := 1
	if r.Role == RoleProvider {
		noLimit = 0
	}

	if !r.Optional && r.Limit == noLimit && r.Scope == ScopeGlobal {
		// All attributes are default, so use the simple string form of the relation.
		return "", r.Interface
	}
	mr := struct {
		Interface string        `yaml:"interface"`
		Limit     *int          `yaml:"limit,omitempty"`
		Optional  bool          `yaml:"optional,omitempty"`
		Scope     RelationScope `yaml:"scope,omitempty"`
	}{
		Interface: r.Interface,
		Optional:  r.Optional,
	}
	if r.Limit != noLimit {
		mr.Limit = &r.Limit
	}
	if r.Scope != ScopeGlobal {
		mr.Scope = r.Scope
	}
	return "", mr
}

// Check checks that the metadata is well-formed.
func (meta Meta) Check() error {
	// Check for duplicate or forbidden relation names or interfaces.
	names := map[string]bool{}
	checkRelations := func(src map[string]Relation, role RelationRole) error {
		for name, rel := range src {
			if rel.Name != name {
				return fmt.Errorf("charm %q has mismatched relation name %q; expected %q", meta.Name, rel.Name, name)
			}
			if rel.Role != role {
				return fmt.Errorf("charm %q has mismatched role %q; expected %q", meta.Name, rel.Role, role)
			}
			// Container-scoped require relations on subordinates are allowed
			// to use the otherwise-reserved juju-* namespace.
			if !meta.Subordinate || role != RoleRequirer || rel.Scope != ScopeContainer {
				if reservedName(name) {
					return fmt.Errorf("charm %q using a reserved relation name: %q", meta.Name, name)
				}
			}
			if role != RoleRequirer {
				if reservedName(rel.Interface) {
					return fmt.Errorf("charm %q relation %q using a reserved interface: %q", meta.Name, name, rel.Interface)
				}
			}
			if names[name] {
				return fmt.Errorf("charm %q using a duplicated relation name: %q", meta.Name, name)
			}
			names[name] = true
		}
		return nil
	}
	if err := checkRelations(meta.Provides, RoleProvider); err != nil {
		return err
	}
	if err := checkRelations(meta.Requires, RoleRequirer); err != nil {
		return err
	}
	if err := checkRelations(meta.Peers, RolePeer); err != nil {
		return err
	}

	// Subordinate charms must have at least one relation that
	// has container scope, otherwise they can't relate to the
	// principal.
	if meta.Subordinate {
		valid := false
		if meta.Requires != nil {
			for _, relationData := range meta.Requires {
				if relationData.Scope == ScopeContainer {
					valid = true
					break
				}
			}
		}
		if !valid {
			return fmt.Errorf("subordinate charm %q lacks \"requires\" relation with container scope", meta.Name)
		}
	}

	if meta.Series != "" {
		if !IsValidSeries(meta.Series) {
			return fmt.Errorf("charm %q declares invalid series: %q", meta.Name, meta.Series)
		}
	}

	names = make(map[string]bool)
	for name, store := range meta.Storage {
		if store.Location != "" && store.Type != StorageFilesystem {
			return fmt.Errorf(`charm %q storage %q: location may not be specified for "type: %s"`, meta.Name, name, store.Type)
		}
		if store.Filesystem != nil && store.Type != StorageFilesystem {
			return fmt.Errorf(`charm %q storage %q: filesystem may not be specified for "type: %s"`, meta.Name, name, store.Type)
		}
		if store.Type == "" {
			return fmt.Errorf("charm %q storage %q: type must be specified", meta.Name, name)
		}
		if store.CountMin < 0 {
			return fmt.Errorf("charm %q storage %q: invalid minimum count %d", meta.Name, name, store.CountMin)
		}
		if store.CountMax == 0 || store.CountMax < -1 {
			return fmt.Errorf("charm %q storage %q: invalid maximum count %d", meta.Name, name, store.CountMax)
		}
		if names[name] {
			return fmt.Errorf("charm %q storage %q: duplicated storage name", meta.Name, name)
		}
		names[name] = true
	}

	return nil
}

func reservedName(name string) bool {
	return name == "juju" || strings.HasPrefix(name, "juju-")
}

func parseRelations(relations interface{}, role RelationRole) map[string]Relation {
	if relations == nil {
		return nil
	}
	result := make(map[string]Relation)
	for name, rel := range relations.(map[string]interface{}) {
		relMap := rel.(map[string]interface{})
		relation := Relation{
			Name:      name,
			Role:      role,
			Interface: relMap["interface"].(string),
			Optional:  relMap["optional"].(bool),
		}
		if scope := relMap["scope"]; scope != nil {
			relation.Scope = RelationScope(scope.(string))
		}
		if relMap["limit"] != nil {
			// Schema defaults to int64, but we know
			// the int range should be more than enough.
			relation.Limit = int(relMap["limit"].(int64))
		}
		result[name] = relation
	}
	return result
}

// Schema coercer that expands the interface shorthand notation.
// A consistent format is easier to work with than considering the
// potential difference everywhere.
//
// Supports the following variants::
//
//   provides:
//     server: riak
//     admin: http
//     foobar:
//       interface: blah
//
//   provides:
//     server:
//       interface: mysql
//       limit:
//       optional: false
//
// In all input cases, the output is the fully specified interface
// representation as seen in the mysql interface description above.
func ifaceExpander(limit interface{}) schema.Checker {
	return ifaceExpC{limit}
}

type ifaceExpC struct {
	limit interface{}
}

var (
	stringC = schema.String()
	mapC    = schema.StringMap(schema.Any())
)

func (c ifaceExpC) Coerce(v interface{}, path []string) (newv interface{}, err error) {
	s, err := stringC.Coerce(v, path)
	if err == nil {
		newv = map[string]interface{}{
			"interface": s,
			"limit":     c.limit,
			"optional":  false,
			"scope":     string(ScopeGlobal),
		}
		return
	}

	v, err = mapC.Coerce(v, path)
	if err != nil {
		return
	}
	m := v.(map[string]interface{})
	if _, ok := m["limit"]; !ok {
		m["limit"] = c.limit
	}
	return ifaceSchema.Coerce(m, path)
}

var ifaceSchema = schema.FieldMap(
	schema.Fields{
		"interface": schema.String(),
		"limit":     schema.OneOf(schema.Const(nil), schema.Int()),
		"scope":     schema.OneOf(schema.Const(string(ScopeGlobal)), schema.Const(string(ScopeContainer))),
		"optional":  schema.Bool(),
	},
	schema.Defaults{
		"scope":    string(ScopeGlobal),
		"optional": false,
	},
)

func parseStorage(stores interface{}) map[string]Storage {
	if stores == nil {
		return nil
	}
	result := make(map[string]Storage)
	for name, store := range stores.(map[string]interface{}) {
		storeMap := store.(map[string]interface{})
		store := Storage{
			Name:       name,
			Type:       StorageType(storeMap["type"].(string)),
			Shared:     storeMap["shared"].(bool),
			ReadOnly:   storeMap["read-only"].(bool),
			Persistent: storeMap["persistent"].(bool),
		}
		required := storeMap["required"].(bool)
		if count, ok := storeMap["count"].([2]int); ok {
			store.CountMin = count[0]
			store.CountMax = count[1]
		} else {
			store.CountMin = -1
			store.CountMax = 1
		}
		if store.CountMin == -1 {
			if required {
				store.CountMin = store.CountMax
			} else {
				store.CountMin = 0
			}
		}
		if loc, ok := storeMap["location"].(string); ok {
			store.Location = loc
		}
		store.Filesystem = parseFilesystem(storeMap["filesystem"])
		result[name] = store
	}
	return result
}

func parseFilesystem(filesystems interface{}) []Filesystem {
	if filesystems == nil {
		return nil
	}
	slice := filesystems.([]interface{})
	result := make([]Filesystem, 0, len(slice))
	for _, elem := range slice {
		switch elem := elem.(type) {
		case string:
			result = append(result, Filesystem{Type: elem})
		case map[string]interface{}:
			fs := Filesystem{
				Type:         elem["type"].(string),
				MountOptions: parseStringList(elem["options"]),
				MkfsOptions:  parseStringList(elem["mkfs-options"]),
			}
			result = append(result, fs)
		}
	}
	return result
}

var storageSchema = schema.FieldMap(
	schema.Fields{
		"required":   schema.Bool(),
		"shared":     schema.Bool(),
		"read-only":  schema.Bool(),
		"persistent": schema.Bool(),
		"count":      storageCountC{}, // m, m-n, m-
		"location":   schema.String(),
		"type":       schema.OneOf(schema.Const(string(StorageBlock)), schema.Const(string(StorageFilesystem))),
		"filesystem": schema.List(schema.OneOf(schema.String(), filesystemSchema)),
	},
	schema.Defaults{
		"required":   false,
		"shared":     false,
		"read-only":  false,
		"persistent": false,
		"count":      schema.Omit,
		"location":   schema.Omit,
		"filesystem": schema.Omit,
	},
)

var filesystemSchema = schema.FieldMap(
	schema.Fields{
		"type":         schema.String(),
		"mkfs-options": schema.List(schema.String()),
		"options":      schema.List(schema.String()),
	},
	schema.Defaults{
		"mkfs-options": schema.Omit,
		"options":      schema.Omit,
	},
)

type storageCountC struct{}

var storageCountRE = regexp.MustCompile("^([0-9]+)-([0-9]*)$")

func (c storageCountC) Coerce(v interface{}, path []string) (newv interface{}, err error) {
	s, err := schema.OneOf(schema.Int(), stringC).Coerce(v, path)
	if err != nil {
		return nil, err
	}
	if m, ok := s.(int64); ok {
		// We've got a count of the form "m": m represents the
		// maximum. The minimum is either 0 or m, depending on the
		// value of "required". Use -1 as a placeholder.
		if m <= 0 {
			return nil, fmt.Errorf("%s: invalid count %v", strings.Join(path[1:], ""), m)
		}
		return [2]int{-1, int(m)}, nil
	}
	match := storageCountRE.FindStringSubmatch(s.(string))
	if match == nil {
		return nil, fmt.Errorf("%s: value %q does not match 'm', 'm-n', or 'm-'", strings.Join(path[1:], ""), s)
	}
	var m, n int
	if m, err = strconv.Atoi(match[1]); err != nil {
		return nil, err
	}
	if len(match[2]) == 0 {
		// We've got a count of the form "m-1": m represents the
		// minimum, and there is no upper bound.
		n = -1
	} else {
		if n, err = strconv.Atoi(match[2]); err != nil {
			return nil, err
		}
	}
	return [2]int{m, n}, nil
}

var charmSchema = schema.FieldMap(
	schema.Fields{
		"name":        schema.String(),
		"summary":     schema.String(),
		"description": schema.String(),
		"peers":       schema.StringMap(ifaceExpander(int64(1))),
		"provides":    schema.StringMap(ifaceExpander(nil)),
		"requires":    schema.StringMap(ifaceExpander(int64(1))),
		"revision":    schema.Int(), // Obsolete
		"format":      schema.Int(),
		"subordinate": schema.Bool(),
		"categories":  schema.List(schema.String()),
		"tags":        schema.List(schema.String()),
		"series":      schema.String(),
		"storage":     schema.StringMap(storageSchema),
	},
	schema.Defaults{
		"provides":    schema.Omit,
		"requires":    schema.Omit,
		"peers":       schema.Omit,
		"revision":    schema.Omit,
		"format":      1,
		"subordinate": schema.Omit,
		"categories":  schema.Omit,
		"tags":        schema.Omit,
		"series":      schema.Omit,
		"storage":     schema.Omit,
	},
)
