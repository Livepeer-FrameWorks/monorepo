.PHONY: generate build run test clean

SCHEME = FrameWorks
BUILD_DIR = build
CONFIG = Debug

generate:
	xcodegen generate

build: generate
	xcodebuild \
		-project FrameWorks.xcodeproj \
		-scheme $(SCHEME) \
		-configuration $(CONFIG) \
		-derivedDataPath $(BUILD_DIR) \
		CODE_SIGN_IDENTITY="-" \
		build

run: build
	open $(BUILD_DIR)/Build/Products/$(CONFIG)/FrameWorks.app

test: generate
	xcodebuild \
		-project FrameWorks.xcodeproj \
		-scheme $(SCHEME) \
		-configuration $(CONFIG) \
		-derivedDataPath $(BUILD_DIR) \
		-destination 'platform=macOS' \
		CODE_SIGN_IDENTITY="-" \
		test

archive: generate
	xcodebuild \
		-project FrameWorks.xcodeproj \
		-scheme $(SCHEME) \
		-configuration Release \
		-archivePath $(BUILD_DIR)/FrameWorks.xcarchive \
		archive

clean:
	rm -rf $(BUILD_DIR) FrameWorks.xcodeproj
