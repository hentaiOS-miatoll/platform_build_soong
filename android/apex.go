// Copyright 2018 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package android

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/blueprint"
)

var (
	SdkVersion_Android10 = uncheckedFinalApiLevel(29)
)

type ApexInfo struct {
	// Name of the apex variation that this module is mutated into
	ApexVariationName string

	// Serialized ApiLevel. Use via MinSdkVersion() method. Cannot be stored in
	// its struct form because this is cloned into properties structs, and
	// ApiLevel has private members.
	MinSdkVersionStr string
	Updatable        bool
	RequiredSdks     SdkRefs

	InApexes []string
}

func (i ApexInfo) mergedName(ctx EarlyModuleContext) string {
	name := "apex" + strconv.Itoa(i.MinSdkVersion(ctx).FinalOrFutureInt())
	for _, sdk := range i.RequiredSdks {
		name += "_" + sdk.Name + "_" + sdk.Version
	}
	return name
}

func (this *ApexInfo) MinSdkVersion(ctx EarlyModuleContext) ApiLevel {
	return ApiLevelOrPanic(ctx, this.MinSdkVersionStr)
}

// Extracted from ApexModule to make it easier to define custom subsets of the
// ApexModule interface and improve code navigation within the IDE.
type DepIsInSameApex interface {
	// DepIsInSameApex tests if the other module 'dep' is installed to the same
	// APEX as this module
	DepIsInSameApex(ctx BaseModuleContext, dep Module) bool
}

// ApexModule is the interface that a module type is expected to implement if
// the module has to be built differently depending on whether the module
// is destined for an apex or not (installed to one of the regular partitions).
//
// Native shared libraries are one such module type; when it is built for an
// APEX, it should depend only on stable interfaces such as NDK, stable AIDL,
// or C APIs from other APEXs.
//
// A module implementing this interface will be mutated into multiple
// variations by apex.apexMutator if it is directly or indirectly included
// in one or more APEXs. Specifically, if a module is included in apex.foo and
// apex.bar then three apex variants are created: platform, apex.foo and
// apex.bar. The platform variant is for the regular partitions
// (e.g., /system or /vendor, etc.) while the other two are for the APEXs,
// respectively.
type ApexModule interface {
	Module
	DepIsInSameApex

	apexModuleBase() *ApexModuleBase

	// Marks that this module should be built for the specified APEX.
	// Call this before apex.apexMutator is run.
	BuildForApex(apex ApexInfo)

	// Returns the name of APEX variation that this module will be built for.
	// Empty string is returned when 'IsForPlatform() == true'. Note that a
	// module can beincluded in multiple APEXes, in which case, the module
	// is mutated into one or more variants, each of which is for one or
	// more APEXes.  This method returns the name of the APEX variation of
	// the module.
	// Call this after apex.apexMutator is run.
	ApexVariationName() string

	// Returns the name of the APEX modules that this variant of this module
	// is present in.
	// Call this after apex.apexMutator is run.
	InApexes() []string

	// Tests whether this module will be built for the platform or not.
	// This is a shortcut for ApexVariationName() == ""
	IsForPlatform() bool

	// Tests if this module could have APEX variants. APEX variants are
	// created only for the modules that returns true here. This is useful
	// for not creating APEX variants for certain types of shared libraries
	// such as NDK stubs.
	CanHaveApexVariants() bool

	// Tests if this module can be installed to APEX as a file. For example,
	// this would return true for shared libs while return false for static
	// libs.
	IsInstallableToApex() bool

	// Mutate this module into one or more variants each of which is built
	// for an APEX marked via BuildForApex().
	CreateApexVariations(mctx BottomUpMutatorContext) []Module

	// Tests if this module is available for the specified APEX or ":platform"
	AvailableFor(what string) bool

	// Return true if this module is not available to platform (i.e. apex_available
	// property doesn't have "//apex_available:platform"), or shouldn't be available
	// to platform, which is the case when this module depends on other module that
	// isn't available to platform.
	NotAvailableForPlatform() bool

	// Mark that this module is not available to platform. Set by the
	// check-platform-availability mutator in the apex package.
	SetNotAvailableForPlatform()

	// Returns the highest version which is <= maxSdkVersion.
	// For example, with maxSdkVersion is 10 and versionList is [9,11]
	// it returns 9 as string
	ChooseSdkVersion(ctx BaseModuleContext, versionList []string, maxSdkVersion ApiLevel) (string, error)

	// Tests if the module comes from an updatable APEX.
	Updatable() bool

	// List of APEXes that this module tests. The module has access to
	// the private part of the listed APEXes even when it is not included in the
	// APEXes.
	TestFor() []string

	// Returns nil if this module supports sdkVersion
	// Otherwise, returns error with reason
	ShouldSupportSdkVersion(ctx BaseModuleContext, sdkVersion ApiLevel) error

	// Returns true if this module needs a unique variation per apex, for example if
	// use_apex_name_macro is set.
	UniqueApexVariations() bool

	// UpdateUniqueApexVariationsForDeps sets UniqueApexVariationsForDeps if any dependencies
	// that are in the same APEX have unique APEX variations so that the module can link against
	// the right variant.
	UpdateUniqueApexVariationsForDeps(mctx BottomUpMutatorContext)
}

type ApexProperties struct {
	// Availability of this module in APEXes. Only the listed APEXes can contain
	// this module. If the module has stubs then other APEXes and the platform may
	// access it through them (subject to visibility).
	//
	// "//apex_available:anyapex" is a pseudo APEX name that matches to any APEX.
	// "//apex_available:platform" refers to non-APEX partitions like "system.img".
	// "com.android.gki.*" matches any APEX module name with the prefix "com.android.gki.".
	// Default is ["//apex_available:platform"].
	Apex_available []string

	Info ApexInfo `blueprint:"mutated"`

	NotAvailableForPlatform bool `blueprint:"mutated"`

	UniqueApexVariationsForDeps bool `blueprint:"mutated"`
}

// Marker interface that identifies dependencies that are excluded from APEX
// contents.
type ExcludeFromApexContentsTag interface {
	blueprint.DependencyTag

	// Method that differentiates this interface from others.
	ExcludeFromApexContents()
}

// Provides default implementation for the ApexModule interface. APEX-aware
// modules are expected to include this struct and call InitApexModule().
type ApexModuleBase struct {
	ApexProperties ApexProperties

	canHaveApexVariants bool

	apexVariationsLock sync.Mutex // protects apexVariations during parallel apexDepsMutator
	apexVariations     []ApexInfo
}

func (m *ApexModuleBase) apexModuleBase() *ApexModuleBase {
	return m
}

func (m *ApexModuleBase) ApexAvailable() []string {
	return m.ApexProperties.Apex_available
}

func (m *ApexModuleBase) TestFor() []string {
	// To be implemented by concrete types inheriting ApexModuleBase
	return nil
}

func (m *ApexModuleBase) UniqueApexVariations() bool {
	return false
}

func (m *ApexModuleBase) UpdateUniqueApexVariationsForDeps(mctx BottomUpMutatorContext) {
	// anyInSameApex returns true if the two ApexInfo lists contain any values in an InApexes list
	// in common.  It is used instead of DepIsInSameApex because it needs to determine if the dep
	// is in the same APEX due to being directly included, not only if it is included _because_ it
	// is a dependency.
	anyInSameApex := func(a, b []ApexInfo) bool {
		collectApexes := func(infos []ApexInfo) []string {
			var ret []string
			for _, info := range infos {
				ret = append(ret, info.InApexes...)
			}
			return ret
		}

		aApexes := collectApexes(a)
		bApexes := collectApexes(b)
		sort.Strings(bApexes)
		for _, aApex := range aApexes {
			index := sort.SearchStrings(bApexes, aApex)
			if index < len(bApexes) && bApexes[index] == aApex {
				return true
			}
		}
		return false
	}

	mctx.VisitDirectDeps(func(dep Module) {
		if depApexModule, ok := dep.(ApexModule); ok {
			if anyInSameApex(depApexModule.apexModuleBase().apexVariations, m.apexVariations) &&
				(depApexModule.UniqueApexVariations() ||
					depApexModule.apexModuleBase().ApexProperties.UniqueApexVariationsForDeps) {
				m.ApexProperties.UniqueApexVariationsForDeps = true
			}
		}
	})
}

func (m *ApexModuleBase) BuildForApex(apex ApexInfo) {
	m.apexVariationsLock.Lock()
	defer m.apexVariationsLock.Unlock()
	for _, v := range m.apexVariations {
		if v.ApexVariationName == apex.ApexVariationName {
			return
		}
	}
	m.apexVariations = append(m.apexVariations, apex)
}

func (m *ApexModuleBase) ApexVariationName() string {
	return m.ApexProperties.Info.ApexVariationName
}

func (m *ApexModuleBase) InApexes() []string {
	return m.ApexProperties.Info.InApexes
}

func (m *ApexModuleBase) IsForPlatform() bool {
	return m.ApexProperties.Info.ApexVariationName == ""
}

func (m *ApexModuleBase) CanHaveApexVariants() bool {
	return m.canHaveApexVariants
}

func (m *ApexModuleBase) IsInstallableToApex() bool {
	// should be overriden if needed
	return false
}

const (
	AvailableToPlatform = "//apex_available:platform"
	AvailableToAnyApex  = "//apex_available:anyapex"
	AvailableToGkiApex  = "com.android.gki.*"
)

func CheckAvailableForApex(what string, apex_available []string) bool {
	if len(apex_available) == 0 {
		// apex_available defaults to ["//apex_available:platform"],
		// which means 'available to the platform but no apexes'.
		return what == AvailableToPlatform
	}
	return InList(what, apex_available) ||
		(what != AvailableToPlatform && InList(AvailableToAnyApex, apex_available)) ||
		(strings.HasPrefix(what, "com.android.gki.") && InList(AvailableToGkiApex, apex_available))
}

func (m *ApexModuleBase) AvailableFor(what string) bool {
	return CheckAvailableForApex(what, m.ApexProperties.Apex_available)
}

func (m *ApexModuleBase) NotAvailableForPlatform() bool {
	return m.ApexProperties.NotAvailableForPlatform
}

func (m *ApexModuleBase) SetNotAvailableForPlatform() {
	m.ApexProperties.NotAvailableForPlatform = true
}

func (m *ApexModuleBase) DepIsInSameApex(ctx BaseModuleContext, dep Module) bool {
	// By default, if there is a dependency from A to B, we try to include both in the same APEX,
	// unless B is explicitly from outside of the APEX (i.e. a stubs lib). Thus, returning true.
	// This is overridden by some module types like apex.ApexBundle, cc.Module, java.Module, etc.
	return true
}

func (m *ApexModuleBase) ChooseSdkVersion(ctx BaseModuleContext, versionList []string, maxSdkVersion ApiLevel) (string, error) {
	for i := range versionList {
		version := versionList[len(versionList)-i-1]
		ver, err := ApiLevelFromUser(ctx, version)
		if err != nil {
			return "", err
		}
		if ver.LessThanOrEqualTo(maxSdkVersion) {
			return version, nil
		}
	}
	return "", fmt.Errorf("not found a version(<=%s) in versionList: %v", maxSdkVersion, versionList)
}

func (m *ApexModuleBase) checkApexAvailableProperty(mctx BaseModuleContext) {
	for _, n := range m.ApexProperties.Apex_available {
		if n == AvailableToPlatform || n == AvailableToAnyApex || n == AvailableToGkiApex {
			continue
		}
		if !mctx.OtherModuleExists(n) && !mctx.Config().AllowMissingDependencies() {
			mctx.PropertyErrorf("apex_available", "%q is not a valid module name", n)
		}
	}
}

func (m *ApexModuleBase) Updatable() bool {
	return m.ApexProperties.Info.Updatable
}

type byApexName []ApexInfo

func (a byApexName) Len() int           { return len(a) }
func (a byApexName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byApexName) Less(i, j int) bool { return a[i].ApexVariationName < a[j].ApexVariationName }

// mergeApexVariations deduplicates APEX variations that would build identically into a common
// variation.  It returns the reduced list of variations and a list of aliases from the original
// variation names to the new variation names.
func mergeApexVariations(ctx EarlyModuleContext, apexVariations []ApexInfo) (merged []ApexInfo, aliases [][2]string) {
	sort.Sort(byApexName(apexVariations))
	seen := make(map[string]int)
	for _, apexInfo := range apexVariations {
		apexName := apexInfo.ApexVariationName
		mergedName := apexInfo.mergedName(ctx)
		if index, exists := seen[mergedName]; exists {
			merged[index].InApexes = append(merged[index].InApexes, apexName)
			merged[index].Updatable = merged[index].Updatable || apexInfo.Updatable
		} else {
			seen[mergedName] = len(merged)
			apexInfo.ApexVariationName = apexInfo.mergedName(ctx)
			apexInfo.InApexes = CopyOf(apexInfo.InApexes)
			merged = append(merged, apexInfo)
		}
		aliases = append(aliases, [2]string{apexName, mergedName})
	}
	return merged, aliases
}

func (m *ApexModuleBase) CreateApexVariations(mctx BottomUpMutatorContext) []Module {
	if len(m.apexVariations) > 0 {
		m.checkApexAvailableProperty(mctx)

		var apexVariations []ApexInfo
		var aliases [][2]string
		if !mctx.Module().(ApexModule).UniqueApexVariations() && !m.ApexProperties.UniqueApexVariationsForDeps {
			apexVariations, aliases = mergeApexVariations(mctx, m.apexVariations)
		} else {
			apexVariations = m.apexVariations
		}

		sort.Sort(byApexName(apexVariations))
		variations := []string{}
		variations = append(variations, "") // Original variation for platform
		for _, apex := range apexVariations {
			variations = append(variations, apex.ApexVariationName)
		}

		defaultVariation := ""
		mctx.SetDefaultDependencyVariation(&defaultVariation)

		modules := mctx.CreateVariations(variations...)
		for i, mod := range modules {
			platformVariation := i == 0
			if platformVariation && !mctx.Host() && !mod.(ApexModule).AvailableFor(AvailableToPlatform) {
				// Do not install the module for platform, but still allow it to output
				// uninstallable AndroidMk entries in certain cases when they have
				// side effects.
				mod.MakeUninstallable()
			}
			if !platformVariation {
				mod.(ApexModule).apexModuleBase().ApexProperties.Info = apexVariations[i-1]
			}
		}

		for _, alias := range aliases {
			mctx.CreateAliasVariation(alias[0], alias[1])
		}

		return modules
	}
	return nil
}

var apexData OncePer
var apexNamesMapMutex sync.Mutex
var apexNamesKey = NewOnceKey("apexNames")

// This structure maintains the global mapping in between modules and APEXes.
// Examples:
//
// apexNamesMap()["foo"]["bar"] == true: module foo is directly depended on by APEX bar
// apexNamesMap()["foo"]["bar"] == false: module foo is indirectly depended on by APEX bar
// apexNamesMap()["foo"]["bar"] doesn't exist: foo is not built for APEX bar
func apexNamesMap() map[string]map[string]bool {
	return apexData.Once(apexNamesKey, func() interface{} {
		return make(map[string]map[string]bool)
	}).(map[string]map[string]bool)
}

// Update the map to mark that a module named moduleName is directly or indirectly
// depended on by the specified APEXes. Directly depending means that a module
// is explicitly listed in the build definition of the APEX via properties like
// native_shared_libs, java_libs, etc.
func UpdateApexDependency(apex ApexInfo, moduleName string, directDep bool) {
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	apexesForModule, ok := apexNamesMap()[moduleName]
	if !ok {
		apexesForModule = make(map[string]bool)
		apexNamesMap()[moduleName] = apexesForModule
	}
	apexesForModule[apex.ApexVariationName] = apexesForModule[apex.ApexVariationName] || directDep
	for _, apexName := range apex.InApexes {
		apexesForModule[apexName] = apexesForModule[apex.ApexVariationName] || directDep
	}
}

// TODO(b/146393795): remove this when b/146393795 is fixed
func ClearApexDependency() {
	m := apexNamesMap()
	for k := range m {
		delete(m, k)
	}
}

// Tests whether a module named moduleName is directly depended on by an APEX
// named apexName.
func DirectlyInApex(apexName string, moduleName string) bool {
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	if apexNamesForModule, ok := apexNamesMap()[moduleName]; ok {
		return apexNamesForModule[apexName]
	}
	return false
}

// Tests whether a module named moduleName is directly depended on by all APEXes
// in a list of apexNames.
func DirectlyInAllApexes(apexNames []string, moduleName string) bool {
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	for _, apexName := range apexNames {
		apexNamesForModule := apexNamesMap()[moduleName]
		if !apexNamesForModule[apexName] {
			return false
		}
	}
	return true
}

type hostContext interface {
	Host() bool
}

// Tests whether a module named moduleName is directly depended on by any APEX.
func DirectlyInAnyApex(ctx hostContext, moduleName string) bool {
	if ctx.Host() {
		// Host has no APEX.
		return false
	}
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	if apexNames, ok := apexNamesMap()[moduleName]; ok {
		for an := range apexNames {
			if apexNames[an] {
				return true
			}
		}
	}
	return false
}

// Tests whether a module named module is depended on (including both
// direct and indirect dependencies) by any APEX.
func InAnyApex(moduleName string) bool {
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	apexNames, ok := apexNamesMap()[moduleName]
	return ok && len(apexNames) > 0
}

func GetApexesForModule(moduleName string) []string {
	ret := []string{}
	apexNamesMapMutex.Lock()
	defer apexNamesMapMutex.Unlock()
	if apexNames, ok := apexNamesMap()[moduleName]; ok {
		for an := range apexNames {
			ret = append(ret, an)
		}
	}
	return ret
}

func InitApexModule(m ApexModule) {
	base := m.apexModuleBase()
	base.canHaveApexVariants = true

	m.AddProperties(&base.ApexProperties)
}

// A dependency info for a single ApexModule, either direct or transitive.
type ApexModuleDepInfo struct {
	// Name of the dependency
	To string
	// List of dependencies To belongs to. Includes APEX itself, if a direct dependency.
	From []string
	// Whether the dependency belongs to the final compiled APEX.
	IsExternal bool
	// min_sdk_version of the ApexModule
	MinSdkVersion string
}

// A map of a dependency name to its ApexModuleDepInfo
type DepNameToDepInfoMap map[string]ApexModuleDepInfo

type ApexBundleDepsInfo struct {
	flatListPath OutputPath
	fullListPath OutputPath
}

type ApexBundleDepsInfoIntf interface {
	Updatable() bool
	FlatListPath() Path
	FullListPath() Path
}

func (d *ApexBundleDepsInfo) FlatListPath() Path {
	return d.flatListPath
}

func (d *ApexBundleDepsInfo) FullListPath() Path {
	return d.fullListPath
}

// Generate two module out files:
// 1. FullList with transitive deps and their parents in the dep graph
// 2. FlatList with a flat list of transitive deps
func (d *ApexBundleDepsInfo) BuildDepsInfoLists(ctx ModuleContext, minSdkVersion string, depInfos DepNameToDepInfoMap) {
	var fullContent strings.Builder
	var flatContent strings.Builder

	fmt.Fprintf(&flatContent, "%s(minSdkVersion:%s):\\n", ctx.ModuleName(), minSdkVersion)
	for _, key := range FirstUniqueStrings(SortedStringKeys(depInfos)) {
		info := depInfos[key]
		toName := fmt.Sprintf("%s(minSdkVersion:%s)", info.To, info.MinSdkVersion)
		if info.IsExternal {
			toName = toName + " (external)"
		}
		fmt.Fprintf(&fullContent, "%s <- %s\\n", toName, strings.Join(SortedUniqueStrings(info.From), ", "))
		fmt.Fprintf(&flatContent, "  %s\\n", toName)
	}

	d.fullListPath = PathForModuleOut(ctx, "depsinfo", "fulllist.txt").OutputPath
	ctx.Build(pctx, BuildParams{
		Rule:        WriteFile,
		Description: "Full Dependency Info",
		Output:      d.fullListPath,
		Args: map[string]string{
			"content": fullContent.String(),
		},
	})

	d.flatListPath = PathForModuleOut(ctx, "depsinfo", "flatlist.txt").OutputPath
	ctx.Build(pctx, BuildParams{
		Rule:        WriteFile,
		Description: "Flat Dependency Info",
		Output:      d.flatListPath,
		Args: map[string]string{
			"content": flatContent.String(),
		},
	})
}

// TODO(b/158059172): remove minSdkVersion allowlist
var minSdkVersionAllowlist = func(apiMap map[string]int) map[string]ApiLevel {
	list := make(map[string]ApiLevel, len(apiMap))
	for name, finalApiInt := range apiMap {
		list[name] = uncheckedFinalApiLevel(finalApiInt)
	}
	return list
}(map[string]int{
	"adbd":                  30,
	"android.net.ipsec.ike": 30,
	"androidx-constraintlayout_constraintlayout-solver": 30,
	"androidx.annotation_annotation":                    28,
	"androidx.arch.core_core-common":                    28,
	"androidx.collection_collection":                    28,
	"androidx.lifecycle_lifecycle-common":               28,
	"apache-commons-compress":                           29,
	"bouncycastle_ike_digests":                          30,
	"brotli-java":                                       29,
	"captiveportal-lib":                                 28,
	"flatbuffer_headers":                                30,
	"framework-permission":                              30,
	"framework-statsd":                                  30,
	"gemmlowp_headers":                                  30,
	"ike-internals":                                     30,
	"kotlinx-coroutines-android":                        28,
	"kotlinx-coroutines-core":                           28,
	"libadb_crypto":                                     30,
	"libadb_pairing_auth":                               30,
	"libadb_pairing_connection":                         30,
	"libadb_pairing_server":                             30,
	"libadb_protos":                                     30,
	"libadb_tls_connection":                             30,
	"libadbconnection_client":                           30,
	"libadbconnection_server":                           30,
	"libadbd_core":                                      30,
	"libadbd_services":                                  30,
	"libadbd":                                           30,
	"libapp_processes_protos_lite":                      30,
	"libasyncio":                                        30,
	"libbrotli":                                         30,
	"libbuildversion":                                   30,
	"libcrypto_static":                                  30,
	"libcrypto_utils":                                   30,
	"libdiagnose_usb":                                   30,
	"libeigen":                                          30,
	"liblz4":                                            30,
	"libmdnssd":                                         30,
	"libneuralnetworks_common":                          30,
	"libneuralnetworks_headers":                         30,
	"libneuralnetworks":                                 30,
	"libprocpartition":                                  30,
	"libprotobuf-java-lite":                             30,
	"libprotoutil":                                      30,
	"libqemu_pipe":                                      30,
	"libstats_jni":                                      30,
	"libstatslog_statsd":                                30,
	"libstatsmetadata":                                  30,
	"libstatspull":                                      30,
	"libstatssocket":                                    30,
	"libsync":                                           30,
	"libtextclassifier_hash_headers":                    30,
	"libtextclassifier_hash_static":                     30,
	"libtflite_kernel_utils":                            30,
	"libwatchdog":                                       29,
	"libzstd":                                           30,
	"metrics-constants-protos":                          28,
	"net-utils-framework-common":                        29,
	"permissioncontroller-statsd":                       28,
	"philox_random_headers":                             30,
	"philox_random":                                     30,
	"service-permission":                                30,
	"service-statsd":                                    30,
	"statsd-aidl-ndk_platform":                          30,
	"statsd":                                            30,
	"tensorflow_headers":                                30,
	"xz-java":                                           29,
})

// Function called while walking an APEX's payload dependencies.
//
// Return true if the `to` module should be visited, false otherwise.
type PayloadDepsCallback func(ctx ModuleContext, from blueprint.Module, to ApexModule, externalDep bool) bool

// UpdatableModule represents updatable APEX/APK
type UpdatableModule interface {
	Module
	WalkPayloadDeps(ctx ModuleContext, do PayloadDepsCallback)
}

// CheckMinSdkVersion checks if every dependency of an updatable module sets min_sdk_version accordingly
func CheckMinSdkVersion(m UpdatableModule, ctx ModuleContext, minSdkVersion ApiLevel) {
	// do not enforce min_sdk_version for host
	if ctx.Host() {
		return
	}

	// do not enforce for coverage build
	if ctx.Config().IsEnvTrue("EMMA_INSTRUMENT") || ctx.DeviceConfig().NativeCoverageEnabled() || ctx.DeviceConfig().ClangCoverageEnabled() {
		return
	}

	// do not enforce deps.min_sdk_version if APEX/APK doesn't set min_sdk_version or
	// min_sdk_version is not finalized (e.g. current or codenames)
	if minSdkVersion.IsCurrent() {
		return
	}

	m.WalkPayloadDeps(ctx, func(ctx ModuleContext, from blueprint.Module, to ApexModule, externalDep bool) bool {
		if externalDep {
			// external deps are outside the payload boundary, which is "stable" interface.
			// We don't have to check min_sdk_version for external dependencies.
			return false
		}
		if am, ok := from.(DepIsInSameApex); ok && !am.DepIsInSameApex(ctx, to) {
			return false
		}
		if err := to.ShouldSupportSdkVersion(ctx, minSdkVersion); err != nil {
			toName := ctx.OtherModuleName(to)
			if ver, ok := minSdkVersionAllowlist[toName]; !ok || ver.GreaterThan(minSdkVersion) {
				ctx.OtherModuleErrorf(to, "should support min_sdk_version(%v) for %q: %v. Dependency path: %s",
					minSdkVersion, ctx.ModuleName(), err.Error(), ctx.GetPathString(false))
				return false
			}
		}
		return true
	})
}
