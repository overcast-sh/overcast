# Build context must be compat/suites/ (parent of dotnet-sdk/) so that the
# shared registry.json can be copied in alongside the C# sources — the same
# convention java-sdk and rust-sdk use. The shared error-fixture corpus
# (compat/model/testdata/errors) is not reachable from this context; see
# compat/AGENTS.md § Where the shared error corpus runs. ScenarioErrorFixtureTests
# detects that and skips itself here rather than failing the build — it runs
# for real in test.yml's compat-suite-unit-tests job, from a full checkout.
FROM mcr.microsoft.com/dotnet/sdk:8.0-alpine AS build

WORKDIR /build
COPY dotnet-sdk/OvercastCompat.csproj ./
COPY dotnet-sdk/Tests/OvercastCompat.Tests.csproj ./Tests/
RUN dotnet restore OvercastCompat.csproj && dotnet restore Tests/OvercastCompat.Tests.csproj

COPY dotnet-sdk/*.cs ./
COPY dotnet-sdk/Harness/ ./Harness/
COPY dotnet-sdk/Clients/ ./Clients/
COPY dotnet-sdk/Groups/ ./Groups/
COPY dotnet-sdk/Registry/ ./Registry/
COPY dotnet-sdk/Scenario/ ./Scenario/
# Sources only: a developer's Tests/bin and Tests/obj would otherwise come along
# with the directory and carry host paths into the build.
COPY dotnet-sdk/Tests/*.cs ./Tests/
RUN dotnet publish OvercastCompat.csproj -c Release -o /out --no-restore

# The SDK type table cmd/compatgen spells emitted C# from: the member types of
# every AWSSDK package pinned above, reflected from the assemblies just
# published. Not part of the image — it is exported to the checkout, where it is
# committed and read offline by `go run -tags dev ./cmd/compatgen`:
#
#   docker build -f compat/suites/dotnet-sdk/Dockerfile --target sdk-types \
#     --output type=local,dest=compat/suites/dotnet-sdk/sdk-types compat/suites
#
# It is a target of its own, ahead of the test stage, because the test stage is
# what fails on a stale table: refreshing one must not need the check it fixes
# to pass first. See compat/suites/dotnet-sdk/README.md § The SDK type table.
FROM build AS sdk-types-render
RUN dotnet /out/OvercastCompat.dll --sdk-types /sdk-types

FROM scratch AS sdk-types
COPY --from=sdk-types-render /sdk-types/ /

FROM build AS test
# SdkTypeTableTests compares the committed table against the assemblies this
# build compiled against, so a pin bump that forgot to refresh it fails here.
COPY dotnet-sdk/sdk-types/ ./sdk-types/
# RegistrationTests (#1700) resolves the suite's real registrations against the
# real registry.json, so it has to be present for the test run below — the
# runtime stage below copies its own for the same reason, one directory later.
COPY registry.json ./
COPY registry.generated.json ./
# Running the registry loader's unit tests here means a loader regression fails
# the image build rather than surfacing as a wrong result in a compat run — the
# guard `mvn package` already gives the java-sdk suite. ScenarioErrorFixtureTests
# comes along too, but the corpus it needs is not in this build context (see
# above), so it skips itself and the build stays green.
RUN dotnet test Tests/OvercastCompat.Tests.csproj -c Release --no-restore

FROM mcr.microsoft.com/dotnet/runtime:8.0-alpine

WORKDIR /app
# From the test stage rather than the build stage: BuildKit builds only the
# stages the target depends on, so copying from here is what makes the image
# wait on the tests passing.
COPY --from=test /out/ ./
# The generated sibling comes along because the loader reads it from the same
# directory. Its absence is not an error — it reads as an empty registry — so an
# image that dropped it would silently run none of the generated groups.
COPY registry.json /registry.json
COPY registry.generated.json /registry.generated.json
ENV OVERCAST_REGISTRY_PATH=/registry.json
ENV OVERCAST_ENDPOINT=http://localhost:4566
ENV OVERCAST_DEFAULT_REGION=us-east-1

ENTRYPOINT ["dotnet", "OvercastCompat.dll"]
