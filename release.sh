#!/bin/bash

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}======================================${NC}"
echo -e "${GREEN}  CC-Connect Release Automation Script${NC}"
echo -e "${GREEN}======================================${NC}"

# 检查是否有未提交的更改
if [ -n "$(git status --porcelain)" ]; then
    echo -e "${RED}Error: Working directory has uncommitted changes${NC}"
    echo "Please commit or stash changes first:"
    git status --short
    exit 1
fi

# 获取当前分支
CURRENT_BRANCH=$(git branch --show-current)
echo -e "${YELLOW}Current branch: $CURRENT_BRANCH${NC}"

# 检查是否在 main 分支
if [ "$CURRENT_BRANCH" != "main" ]; then
    echo -e "${YELLOW}Warning: You are not on 'main' branch${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# 询问版本号
echo ""
read -p "Enter version (e.g., v1.2.3 or v1.2.3-beta.1): " VERSION

if [[ ! $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
    echo -e "${RED}Error: Invalid version format${NC}"
    echo "Expected format: v1.2.3 or v1.2.3-beta.1"
    exit 1
fi

# 提取版本号（去掉 v 前缀）
VERSION_NO_V=${VERSION#v}

echo ""
echo -e "${GREEN}======================================${NC}"
echo -e "${GREEN}Release Summary:${NC}"
echo -e "${GREEN}======================================${NC}"
echo -e "Version: ${YELLOW}$VERSION${NC}"
echo -e "Branch:  ${YELLOW}$CURRENT_BRANCH${NC}"
echo ""

# 确认发布
read -p "Proceed with release? (y/N) " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Release cancelled"
    exit 0
fi

# 1. 更新 npm/package.json 版本号
echo ""
echo -e "${YELLOW}Step 1: Updating npm/package.json version...${NC}"
sed -i '' "s/\"version\": \".*\"/\"version\": \"$VERSION_NO_V\"/" npm/package.json
echo -e "${GREEN}✓ Updated npm/package.json to $VERSION_NO_V${NC}"

# 2. 提交版本更新
echo ""
echo -e "${YELLOW}Step 2: Committing version update...${NC}"
git add npm/package.json
git commit -m "chore: bump version to $VERSION_NO_V"
echo -e "${GREEN}✓ Committed version update${NC}"

# 3. 创建 git tag
echo ""
echo -e "${YELLOW}Step 3: Creating git tag $VERSION...${NC}"
git tag -a "$VERSION" -m "Release $VERSION"
echo -e "${GREEN}✓ Created tag $VERSION${NC}"

# 4. 推送分支和 tag
echo ""
echo -e "${YELLOW}Step 4: Pushing to GitHub...${NC}"
git push origin "$CURRENT_BRANCH"
git push origin "$VERSION"
echo -e "${GREEN}✓ Pushed to GitHub${NC}"

# 5. 等待 GitHub Actions 完成
echo ""
echo -e "${YELLOW}Step 5: GitHub Actions is building release...${NC}"
echo -e "You can monitor progress at:"
echo -e "${CYAN}https://github.com/chenhg5/cc-connect/actions${NC}"
echo ""
echo "Waiting for release to be created..."

# 等待 release 出现
MAX_WAIT=300  # 最多等待5分钟
WAIT_TIME=0
while [ $WAIT_TIME -lt $MAX_WAIT ]; do
    if gh release view "$VERSION" &>/dev/null; then
        echo -e "${GREEN}✓ Release $VERSION created successfully!${NC}"
        break
    fi
    sleep 5
    WAIT_TIME=$((WAIT_TIME + 5))
    echo -n "."
done

if [ $WAIT_TIME -ge $MAX_WAIT ]; then
    echo ""
    echo -e "${YELLOW}Timeout waiting for release${NC}"
    echo "Please check manually at:"
    echo "https://github.com/chenhg5/cc-connect/releases"
fi

# 6. 发布到 npm
echo ""
echo -e "${YELLOW}Step 6: Publishing to npm...${NC}"
read -p "Publish to npm now? (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    cd npm
    npm publish
    echo -e "${GREEN}✓ Published to npm${NC}"
else
    echo -e "${YELLOW}Skipped npm publish${NC}"
    echo "You can publish manually later:"
    echo "  cd npm && npm publish"
fi

echo ""
echo -e "${GREEN}======================================${NC}"
echo -e "${GREEN}  Release Complete!${NC}"
echo -e "${GREEN}======================================${NC}"
echo -e "Release: ${CYAN}https://github.com/chenhg5/cc-connect/releases/tag/$VERSION${NC}"
echo ""
