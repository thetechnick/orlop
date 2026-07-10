package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced

// Role contains rules that represent a set of permissions.
// Permissions are purely additive (there are no "deny" rules).
type Role struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Rules holds all the PolicyRules for this Role.
	Rules []PolicyRule `json:"rules"`
}

// +kubebuilder:object:root=true

// RoleList contains a list of Role.
type RoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Role `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// ClusterRole is a cluster level, logical grouping of PolicyRules that can be referenced as a unit by a RoleBinding or ClusterRoleBinding.
type ClusterRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Rules holds all the PolicyRules for this ClusterRole.
	Rules []PolicyRule `json:"rules"`
}

// +kubebuilder:object:root=true

// ClusterRoleList is a collection of ClusterRoles.
type ClusterRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRole `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced

// RoleBinding references a role, but does not contain it.
// It can reference a Role in the same namespace or a ClusterRole in the global namespace.
// It adds who information via Subjects and namespace information by which namespace it exists in.
type RoleBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Subjects holds references to the objects the role applies to.
	Subjects []Subject `json:"subjects,omitempty"`

	// RoleRef can reference a Role in the current namespace or a ClusterRole in the global namespace.
	// If the RoleRef cannot be resolved, the Authorizer must return an error.
	RoleRef RoleRef `json:"roleRef"`
}

// +kubebuilder:object:root=true

// RoleBindingList is a collection of RoleBindings.
type RoleBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleBinding `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// ClusterRoleBinding references a ClusterRole, but not contain it.
// It can reference a ClusterRole in the global namespace.
// It adds who information via Subjects.
type ClusterRoleBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Subjects holds references to the objects the role applies to.
	Subjects []Subject `json:"subjects,omitempty"`

	// RoleRef can only reference a ClusterRole in the global namespace.
	// If the RoleRef cannot be resolved, the Authorizer must return an error.
	RoleRef RoleRef `json:"roleRef"`
}

// +kubebuilder:object:root=true

// ClusterRoleBindingList is a collection of ClusterRoleBindings.
type ClusterRoleBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRoleBinding `json:"items"`
}

// Subject contains a reference to the object or user identities a role binding applies to.
type Subject struct {
	// Kind of object being referenced. Values defined by this API group are "User", "Group", and "ServiceAccount".
	// If the Authorizer does not recognize the kind value, the Authorizer should report an error.
	Kind string `json:"kind"`

	// APIGroup holds the API group of the referenced subject.
	// Defaults to "" for ServiceAccount subjects.
	// Defaults to "rbac.authorization.k8s.io" for User and Group subjects.
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// Name of the object being referenced.
	Name string `json:"name"`

	// Namespace of the referenced object. If the object kind is non-namespace, such as "User" or "Group", and this value is not empty
	// the Authorizer should report an error.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// RoleRef contains information that points to the role being used.
type RoleRef struct {
	// APIGroup is the group for the resource being referenced.
	APIGroup string `json:"apiGroup"`

	// Kind is the type of resource being referenced.
	Kind string `json:"kind"`

	// Name is the name of resource being referenced.
	Name string `json:"name"`
}

// PolicyRule holds information that describes a policy rule, but does not contain information
// about who the rule applies to or which namespace the rule applies to.
type PolicyRule struct {
	// Verbs is a list of Verbs that apply to ALL the ResourceKinds contained in this rule.
	// '*' represents all verbs.
	Verbs []string `json:"verbs"`

	// APIGroups is the name of the APIGroup that contains the resources.
	// If multiple API groups are specified, any action requested against one of the enumerated resources in any API group will be allowed.
	// '*' represents all API groups.
	// +optional
	APIGroups []string `json:"apiGroups,omitempty"`

	// Resources is a list of resources this rule applies to.
	// '*' represents all resources.
	// +optional
	Resources []string `json:"resources,omitempty"`

	// ResourceNames is an optional white list of names that the rule applies to.
	// An empty set means that everything is allowed.
	// +optional
	ResourceNames []string `json:"resourceNames,omitempty"`

	// NonResourceURLs is a set of partial urls that a user should have access to.
	// *s are allowed, but only as the full, final step in the path.
	// "*" means all.
	// +optional
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
}

