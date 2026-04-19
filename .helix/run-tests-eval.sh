#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

DOCKERFILE="$SCRIPT_DIR/Dockerfile.helix"

die()  { echo "Error: $*" >&2; exit 1; }
info() { echo "$*" >&2; }

# ── Parse test command from Dockerfile.helix (CMD or ENTRYPOINT) ──

parse_instruction() {
    local raw="$1"
    if [[ "$raw" == \[* ]]; then
        local inner="${raw#\[}"
        inner="${inner%\]}"
        local result="" elem=""
        while IFS= read -r -d ',' elem || [[ -n "$elem" ]]; do
            elem=$(echo "$elem" | sed 's/^[[:space:]]*"//; s/"[[:space:]]*$//')
            [[ -z "$elem" ]] && continue
            if [[ "$elem" == *" "* ]]; then
                elem="${elem//\\/\\\\}"
                elem="${elem//\"/\\\"}"
                result+="\"${elem}\" "
            else
                result+="$elem "
            fi
        done <<< "$inner"
        echo "$result" | sed 's/ *$//'
    else
        echo "$raw"
    fi
}

extract_cmd() {
    local cmd_line="" ep_line=""
    while IFS= read -r line || [[ -n "$line" ]]; do
        local trimmed="${line#"${line%%[![:space:]]*}"}"
        case "${trimmed}" in
            CMD\ *|cmd\ *)
                cmd_line="${trimmed:4}"; cmd_line="${cmd_line#"${cmd_line%%[![:space:]]*}"}" ;;
            ENTRYPOINT\ *|entrypoint\ *)
                ep_line="${trimmed:11}"; ep_line="${ep_line#"${ep_line%%[![:space:]]*}"}" ;;
        esac
    done < "$DOCKERFILE"

    if [[ -n "$cmd_line" ]]; then
        local cmd; cmd=$(parse_instruction "$cmd_line")
        if [[ -n "$ep_line" ]]; then
            local ep; ep=$(parse_instruction "$ep_line")
            echo "${ep} ${cmd}"
        else
            echo "$cmd"
        fi
    elif [[ -n "$ep_line" ]]; then
        parse_instruction "$ep_line"
    else
        die "No CMD or ENTRYPOINT found in $DOCKERFILE"
    fi
}

# Unwrap common shell entrypoints like: sh -c "go test ./..."
canonicalize_cmd() {
    local cmd="$1"
    case "$cmd" in
        sh\ -c\ \"*\"|bash\ -c\ \"*\"|/bin/sh\ -c\ \"*\"|/bin/bash\ -c\ \"*\")
            cmd="${cmd#* -c \"}"
            cmd="${cmd%\"}"
            echo "$cmd"
            ;;
        sh\ -c\ \'*\'|bash\ -c\ \'*\'|/bin/sh\ -c\ \'*\'|/bin/bash\ -c\ \'*\')
            cmd="${cmd#* -c \'}"
            cmd="${cmd%\'}"
            echo "$cmd"
            ;;
        *)
            echo "$cmd"
            ;;
    esac
}

# ── Resolve yarn/npm/pnpm script aliases ─────────────────────────

resolve_script() {
    local cmd="$1" script_name=""

    if [[ "$cmd" =~ ^(yarn|pnpm)[[:space:]]+([a-zA-Z][^[:space:]]*) ]]; then
        script_name="${BASH_REMATCH[2]}"
    elif [[ "$cmd" =~ ^(npm|pnpm)[[:space:]]+run[[:space:]]+([^[:space:]]+) ]]; then
        script_name="${BASH_REMATCH[2]}"
    elif [[ "$cmd" =~ ^npm[[:space:]]+test ]]; then
        script_name="test"
    fi

    if [[ -z "$script_name" ]]; then echo "$cmd"; return; fi
    if [[ ! -f "package.json" ]]; then echo "$cmd"; return; fi

    local resolved=""
    if command -v node &>/dev/null; then
        resolved=$(node -e "
            try { console.log(require('./package.json').scripts['$script_name'] || '') }
            catch(e) { console.log('') }
        " 2>/dev/null) || true
    elif command -v python3 &>/dev/null; then
        resolved=$(python3 -c "
import json
print(json.load(open('package.json')).get('scripts',{}).get('$script_name',''))
" 2>/dev/null) || true
    fi

    [[ -n "$resolved" ]] && echo "$resolved" || echo "$cmd"
}

# ── Detect the underlying test runner ─────────────────────────────

detect_runner() {
    local cmd="$1"
    case "$cmd" in
        pytest*|*" pytest "*|python*-m*pytest*)   echo "pytest" ;;
        *vitest*)                                  echo "vitest" ;;
        *jest*)                                    echo "jest" ;;
        *mocha*)                                   echo "mocha" ;;
        go\ test*)                                 echo "go" ;;
        cargo\ test*|cargo\ nextest*)              echo "cargo" ;;
        mvn\ *|./mvnw\ *)                         echo "maven" ;;
        *gradle*test*|*gradlew*test*)              echo "gradle" ;;
        *rspec*)                                   echo "rspec" ;;
        *phpunit*)                                 echo "phpunit" ;;
        dotnet\ test*)                             echo "dotnet" ;;
        mix\ test*)                                echo "mix" ;;
        swift\ test*)                              echo "swift" ;;
        sbt*test*)                                 echo "sbt" ;;
        *nose2*)                                   echo "nose2" ;;
        ruby*-I*|ruby*test*|rake\ test*)           echo "ruby" ;;
        *ctest*|*" ctest "*)                       echo "ctest" ;;
        meson\ test*|*"meson test"*)               echo "meson" ;;
        *)                                         echo "unknown" ;;
    esac
}

normalize_go_full_cmd() {
    local cmd="$1"
    local suffix=""
    local tags=""

    [[ ! "$cmd" =~ ^go[[:space:]]+test(.*)$ ]] && { echo "$cmd"; return; }
    suffix="${BASH_REMATCH[1]}"
    if [[ ! "$suffix" =~ (^|[[:space:]])-tags([=[:space:]]) ]]; then
        tags=" -tags no_web"
    fi

    # Replace ./... with an explicit package list so we can exclude known
    # baseline-incompatible packages across branches.
    suffix=$(echo "$suffix" | sed -E 's@(^|[[:space:]])\./\.\.\.([[:space:]]|$)@ @g')
    echo "go test${tags}${suffix} \$(go list -tags no_web ./... | grep -Ev '/platform/wecom$|/web$')"
}

# ── Strip coverage flags (prevent coverage gates from failing eval) ──

strip_coverage_flags() {
    local runner="$1" cmd="$2"
    case "$runner" in
        pytest)   cmd=$(echo "$cmd" | sed -E 's/ --cov[=-][^ ]*//g; s/ --cov\b//g; s/ --no-cov-on-fail//g') ;;
        jest|vitest) cmd=$(echo "$cmd" | sed -E 's/ --coverage[^ ]*//g') ;;
        go)       cmd=$(echo "$cmd" | sed -E 's/ -cover\b//g; s/ -coverprofile[= ][^ ]*//g; s/ -coverpkg[= ][^ ]*//g') ;;
        phpunit)  cmd=$(echo "$cmd" | sed -E 's/ --coverage-[^ ]*//g') ;;
        mix)      cmd=$(echo "$cmd" | sed -E 's/ --cover\b//g') ;;
        dotnet)   cmd=$(echo "$cmd" | sed -E 's/ --collect:[^ ]*//g; s| /p:CollectCoverage=[^ ]*||g; s| /p:Threshold=[^ ]*||g') ;;
    esac
    echo "$cmd"
}

# ── JVM: convert file path to fully-qualified class name ──────────

path_to_classname() {
    local f="$1"
    for prefix in src/test/java/ src/test/kotlin/ src/test/scala/ \
                  src/main/java/ src/main/kotlin/ src/main/scala/ \
                  src/it/java/ src/it/kotlin/ src/it/scala/; do
        f="${f#$prefix}"
    done
    for ext in .java .kt .scala .groovy .kts; do
        f="${f%$ext}"
    done
    echo "${f//\//.}"
}

# Non-test .go sources in a single package directory (required for file-scoped go test).
go_non_test_sources() {
    local dir="$1"
    find "$dir" -maxdepth 1 -type f -name '*.go' ! -name '*_test.go' 2>/dev/null | LC_ALL=C sort
}

# Targeted Go mode: compile/run tests using only the listed *_test.go files plus all
# non-test sources in the same directory. Unlisted *_test.go files in that package are
# not included (unlike `go test ./pkg -run`, which still builds every *_test.go).
build_go_targeted_cmd() {
    local full_cmd="$1"
    shift
    local files=("$@")
    local suffix=""
    local tags=""
    local segments=()
    local f nf d seen_dir idx
    local dirs=()
    local tests_per_dir=()
    local args

    if [[ "$full_cmd" =~ ^go[[:space:]]+test(.*)$ ]]; then
        suffix="${BASH_REMATCH[1]}"
        suffix=$(echo "$suffix" | sed -E 's@(^|[[:space:]])(\./\.\.|\.|\.\/[^[:space:]]+)([[:space:]]|$)@ @g')
        suffix=$(echo "$suffix" | sed -E 's/[[:space:]]+/ /g; s/^[[:space:]]+|[[:space:]]+$//g')
        if [[ ! "$suffix" =~ (^|[[:space:]])-tags([=[:space:]]) ]]; then
            tags="-tags no_web"
        fi
    fi

    for f in "${files[@]}"; do
        [[ "$f" != *.go ]] && continue
        nf="${f#./}"
        [[ ! -f "$nf" ]] && continue
        [[ "$nf" != *_test.go ]] && continue
        d=$(dirname "$nf")
        [[ "$d" == "." ]] && d="."

        seen_dir=-1
        for idx in "${!dirs[@]}"; do
            if [[ "${dirs[$idx]}" == "$d" ]]; then
                seen_dir=$idx
                break
            fi
        done
        if [[ $seen_dir -ge 0 ]]; then
            if ! printf '%s\n' "${tests_per_dir[$seen_dir]}" | grep -Fxq "$nf" 2>/dev/null; then
                tests_per_dir[$seen_dir]="${tests_per_dir[$seen_dir]}"$'\n'"$nf"
            fi
        else
            dirs+=("$d")
            tests_per_dir+=("$nf")
        fi
    done

    if [[ ${#dirs[@]} -eq 0 ]]; then
        die "No Go *_test.go files in targeted file list"
    fi

    for idx in "${!dirs[@]}"; do
        d="${dirs[$idx]}"
        args=()
        while IFS= read -r src || [[ -n "$src" ]]; do
            [[ -z "$src" ]] && continue
            args+=("$(printf '%q' "$src")")
        done < <(go_non_test_sources "$d")

        if [[ ${#args[@]} -eq 0 ]]; then
            die "No non-test Go sources found in $d"
        fi

        while IFS= read -r src || [[ -n "$src" ]]; do
            [[ -z "$src" ]] && continue
            args+=("$(printf '%q' "$src")")
        done <<< "${tests_per_dir[$idx]}"

        segments+=("go test ${tags} ${suffix} ${args[*]}")
    done

    printf '%s && ' "${segments[@]}" | sed 's/ && $//'
}

# ── Construct the test command for a specific set of files ────────

build_targeted_cmd() {
    local runner="$1" full_cmd="$2"
    shift 2
    local files=("$@")

    case "$runner" in
        pytest)   echo "pytest ${files[*]}" ;;
        vitest)   echo "vitest run ${files[*]}" ;;
        jest)     echo "npx jest --watchAll=false ${files[*]}" ;;
        mocha)    echo "npx mocha ${files[*]}" ;;
        nose2)    echo "nose2 ${files[*]}" ;;
        mix)      echo "mix test ${files[*]}" ;;
        ruby)     echo "ruby ${files[*]}" ;;
        rspec)
            if command -v bundle &>/dev/null; then
                echo "bundle exec rspec ${files[*]}"
            else
                echo "rspec ${files[*]}"
            fi
            ;;
        phpunit)
            if [[ -f "./vendor/bin/phpunit" ]]; then
                echo "./vendor/bin/phpunit ${files[*]}"
            else
                echo "phpunit ${files[*]}"
            fi
            ;;
        go)
            build_go_targeted_cmd "$full_cmd" "${files[@]}"
            ;;
        cargo)
            local args=""
            for f in "${files[@]}"; do
                args+=" --test $(basename "${f}" .rs)"
            done
            echo "cargo test${args}"
            ;;
        maven)
            local classes=()
            for f in "${files[@]}"; do classes+=("$(path_to_classname "$f")"); done
            local joined; joined="$(IFS=,; echo "${classes[*]}")"
            [[ -f "./mvnw" ]] && echo "./mvnw test -Dtest=${joined}" || echo "mvn test -Dtest=${joined}"
            ;;
        gradle)
            local args=""
            for f in "${files[@]}"; do args+=" --tests $(path_to_classname "$f")"; done
            [[ -f "./gradlew" ]] && echo "./gradlew test${args}" || echo "gradle test${args}"
            ;;
        dotnet)
            local parts=()
            for f in "${files[@]}"; do parts+=("FullyQualifiedName~$(basename "$f" .cs)"); done
            local filter; filter="$(IFS='|'; echo "${parts[*]}")"
            echo "dotnet test --filter \"${filter}\""
            ;;
        swift)
            local names=()
            for f in "${files[@]}"; do names+=("$(basename "$f" .swift)"); done
            local joined; joined="$(IFS=,; echo "${names[*]}")"
            echo "swift test --filter ${joined}"
            ;;
        sbt)
            local names=()
            for f in "${files[@]}"; do names+=("$(path_to_classname "$f")"); done
            echo "sbt 'testOnly ${names[*]}'"
            ;;
        ctest)
            # Subset by CTest regex (-R): join basenames (strip common source extensions).
            local rx="" b f
            for f in "${files[@]}"; do
                b=$(basename "$f")
                b="${b%.cpp}"; b="${b%.cc}"; b="${b%.cxx}"; b="${b%.c}"; b="${b%.hpp}"
                [[ -n "$rx" ]] && rx+="|"
                rx+="$b"
            done
            if [[ "$full_cmd" =~ ^[[:space:]]*ctest(.*)$ ]]; then
                echo "ctest -R $(printf '%q' "$rx")${BASH_REMATCH[1]}"
            else
                echo "ctest -R $(printf '%q' "$rx") --test-dir build --output-on-failure"
            fi
            ;;
        meson)
            # Best-effort: meson test accepts test names as positional args (project-defined).
            local names=()
            for f in "${files[@]}"; do names+=("$(basename "$f" .cpp)"); done
            echo "meson test -C builddir --print-errorlogs ${names[*]}"
            ;;
        *)
            info "Warning: unknown runner — appending files to original command"
            echo "${full_cmd} ${files[*]}"
            ;;
    esac
}

# ── Main ──────────────────────────────────────────────────────────

[[ ! -f "$DOCKERFILE" ]] && die "$DOCKERFILE not found in $(pwd)"

FULL_CMD=$(extract_cmd)
FULL_CMD=$(canonicalize_cmd "$FULL_CMD")
RESOLVED=$(resolve_script "$FULL_CMD")
RUNNER=$(detect_runner "$RESOLVED")

if [[ $# -eq 0 || -z "${1:-}" ]]; then
    FULL_CMD=$(strip_coverage_flags "$RUNNER" "$FULL_CMD")
    if [[ "$RUNNER" == "go" ]]; then
        FULL_CMD=$(normalize_go_full_cmd "$FULL_CMD")
    fi
    info "=== Full test suite ==="
    info "Command: $FULL_CMD"
    info "========================"
    eval "$FULL_CMD"
else
    IFS=',' read -ra TEST_FILES <<< "$1"

    TARGETED=$(build_targeted_cmd "$RUNNER" "$FULL_CMD" "${TEST_FILES[@]}")
    TARGETED=$(strip_coverage_flags "$RUNNER" "$TARGETED")

    info "=== Targeted tests ==="
    info "Runner:  $RUNNER"
    info "Files:   ${TEST_FILES[*]}"
    info "Command: $TARGETED"
    info "========================"
    eval "$TARGETED"
fi
