export type CatalogCategory =
  | "storage"
  | "compute"
  | "messaging"
  | "security"
  | "networking"
  | "monitoring"
  | "analytics"
  | "ml"
  | "iot"
  | "media"
  | "migration"
  | "devtools"
  | "management"
  | "business"
  | "billing"
  | "gametech"
  | "robotics"
  | "quantum"
  | "satellite"

export const CATALOG_CATEGORY_LABELS: Record<CatalogCategory, string> = {
  storage: "Storage & Database",
  compute: "Compute",
  messaging: "Messaging",
  security: "Security & Identity",
  networking: "Networking & APIs",
  monitoring: "Monitoring & Observability",
  analytics: "Analytics & Big Data",
  ml: "Machine Learning & AI",
  iot: "IoT & Edge",
  media: "Media Services",
  migration: "Migration & Transfer",
  devtools: "Developer Tools",
  management: "Management & Governance",
  business: "Business Applications",
  billing: "Billing & Cost Management",
  gametech: "Game Tech",
  robotics: "Robotics",
  quantum: "Quantum Computing",
  satellite: "Satellite & Space",
}

export interface CatalogEntry {
  /** Lowercase ID, matches the AWS service's common short name */
  id: string
  /** Human-readable display label */
  label: string
  category: CatalogCategory
  /** 1-2 sentence description of what this AWS service does */
  description: string
  /** Link to official AWS documentation */
  awsDocsUrl: string
  /** Brief explanation of why Overcast doesn't support this service */
  reason: string
  /**
   * Current tier from Overcast's perspective.
   * "stub" = registered with only a minimal discovery or control-plane subset;
   * unsupported operations return 501.
   * "unsupported" = not in Overcast at all.
   */
  tier: "stub" | "unsupported"
  /**
   * Aspirational tier. Most services remain "unsupported" but some could
   * reach "stub" or even "partial" if there is user demand.
   */
  goalTier: "unsupported" | "stub" | "partial"
}

export const CATALOG: CatalogEntry[] = [
  // ── Minimal backend stubs ─────────────────────────────────────────────
  // Shield has a small metadata/probe subset and uses the same generic service
  // page as services that are wholly unsupported.
  {
    id: "shield",
    label: "Shield",
    category: "security",
    description:
      "DDoS protection — Shield Standard (free) and Shield Advanced subscriptions protect against distributed denial-of-service attacks.",
    awsDocsUrl: "https://docs.aws.amazon.com/waf/latest/developerguide/shield-chapter.html",
    reason:
      "Shield provides a minimal active-subscription response and protection metadata CRUD for SDK and CloudFormation workflows. It does not emulate DDoS protection; unsupported operations return 501.",
    tier: "stub",
    goalTier: "stub",
  },

  // ── Analytics & Big Data ───────────────────────────────────────────────
  {
    id: "redshift",
    label: "Amazon Redshift",
    category: "analytics",
    description:
      "Petabyte-scale cloud data warehouse for running complex SQL analytics across structured and semi-structured data.",
    awsDocsUrl: "https://docs.aws.amazon.com/redshift/latest/mgmt/",
    reason:
      "Redshift requires a full distributed query engine (PostgreSQL-compatible with columnar storage). This is not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "emr",
    label: "Amazon EMR",
    category: "analytics",
    description:
      "Managed big data platform for running Apache Spark, Hadoop, Hive, and other large-scale data processing frameworks.",
    awsDocsUrl: "https://docs.aws.amazon.com/emr/latest/ManagementGuide/",
    reason:
      "EMR requires a distributed compute cluster with Spark/Hadoop runtimes. Not feasible to emulate in a lightweight local container.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "quicksight",
    label: "Amazon QuickSight",
    category: "analytics",
    description:
      "Cloud-powered business intelligence service for creating interactive dashboards, visualizations, and ML-powered insights.",
    awsDocsUrl: "https://docs.aws.amazon.com/quicksight/latest/user/",
    reason:
      "QuickSight is a SaaS visualization product. There is no meaningful way to emulate its dashboard rendering and ML insights locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lakeformation",
    label: "AWS Lake Formation",
    category: "analytics",
    description:
      "Service that makes it easy to set up, secure, and manage data lakes. Builds on Glue, S3, and IAM for fine-grained access.",
    awsDocsUrl: "https://docs.aws.amazon.com/lake-formation/latest/dg/",
    reason:
      "Lake Formation depends on Glue, S3, and IAM enforcement working together. Not practical without those services being more fully implemented.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "kinesisanalytics",
    label: "Kinesis Data Analytics",
    category: "analytics",
    description:
      "Real-time stream analytics service powered by Apache Flink for processing and querying streaming data.",
    awsDocsUrl: "https://docs.aws.amazon.com/kinesisanalytics/latest/java/",
    reason:
      "Kinesis Data Analytics requires a full Apache Flink runtime. Kinesis Data Streams itself is supported in Overcast.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "cloudsearch",
    label: "Amazon CloudSearch",
    category: "analytics",
    description:
      "Legacy managed search service for websites and applications (largely superseded by OpenSearch).",
    awsDocsUrl: "https://docs.aws.amazon.com/cloudsearch/latest/developerguide/",
    reason:
      "CloudSearch is a legacy service superseded by OpenSearch. Not a priority for local emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "dataexchange",
    label: "AWS Data Exchange",
    category: "analytics",
    description:
      "Marketplace for finding, subscribing to, and using third-party data directly in AWS.",
    awsDocsUrl: "https://docs.aws.amazon.com/data-exchange/latest/userguide/",
    reason:
      "Data Exchange is a marketplace/SaaS product dependent on third-party data providers. Not applicable for local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "datazone",
    label: "Amazon DataZone",
    category: "analytics",
    description:
      "Data governance service for discovering, sharing, and managing data across organizational boundaries with fine-grained access control.",
    awsDocsUrl: "https://docs.aws.amazon.com/datazone/latest/userguide/",
    reason:
      "DataZone is a managed data governance platform requiring multi-account, organizational-level infrastructure.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "entityresolution",
    label: "AWS Entity Resolution",
    category: "analytics",
    description:
      "ML-powered record matching and linking service for resolving customer and product identities across datasets.",
    awsDocsUrl: "https://docs.aws.amazon.com/entityresolution/latest/userguide/",
    reason:
      "Entity Resolution is an ML-powered managed service. Its matching logic is not practical to replicate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Machine Learning & AI ──────────────────────────────────────────────
  {
    id: "sagemaker",
    label: "Amazon SageMaker",
    category: "ml",
    description:
      "Fully managed platform for building, training, and deploying machine learning models at scale.",
    awsDocsUrl: "https://docs.aws.amazon.com/sagemaker/latest/dg/",
    reason:
      "SageMaker requires GPU compute infrastructure, managed notebook servers, and distributed training. Not feasible to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "bedrock",
    label: "Amazon Bedrock",
    category: "ml",
    description:
      "Fully managed service for accessing foundation models (LLMs) from Amazon, Anthropic, and other providers via API.",
    awsDocsUrl: "https://docs.aws.amazon.com/bedrock/latest/userguide/",
    reason:
      "Bedrock proxies to hosted LLM models. A stub could be added to simulate API responses, but model output cannot be replicated locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "comprehend",
    label: "Amazon Comprehend",
    category: "ml",
    description:
      "Natural language processing service for extracting insights like entities, sentiment, and key phrases from text.",
    awsDocsUrl: "https://docs.aws.amazon.com/comprehend/latest/dg/",
    reason:
      "Comprehend is an ML inference service. Providing meaningful NLP output requires a large language model.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "rekognition",
    label: "Amazon Rekognition",
    category: "ml",
    description:
      "Computer vision service for image and video analysis — detecting objects, faces, text, and content moderation.",
    awsDocsUrl: "https://docs.aws.amazon.com/rekognition/latest/dg/",
    reason:
      "Rekognition is an ML inference service requiring specialized vision models. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "textract",
    label: "Amazon Textract",
    category: "ml",
    description:
      "ML-powered document processing service for extracting text, tables, and forms from scanned documents.",
    awsDocsUrl: "https://docs.aws.amazon.com/textract/latest/dg/",
    reason: "Textract uses specialized OCR and layout models. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "transcribe",
    label: "Amazon Transcribe",
    category: "ml",
    description: "Automatic speech recognition service that converts audio and video to text.",
    awsDocsUrl: "https://docs.aws.amazon.com/transcribe/latest/dg/",
    reason:
      "Transcribe requires speech recognition models. Not feasible to replicate locally without bundling a large ASR model.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "polly",
    label: "Amazon Polly",
    category: "ml",
    description:
      "Text-to-speech service that turns text into lifelike speech using deep learning voices.",
    awsDocsUrl: "https://docs.aws.amazon.com/polly/latest/dg/",
    reason:
      "Polly requires TTS synthesis models. Not feasible to replicate locally without bundling a large TTS model.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "translate",
    label: "Amazon Translate",
    category: "ml",
    description: "Neural machine translation service for fast, high-quality language translation.",
    awsDocsUrl: "https://docs.aws.amazon.com/translate/latest/dg/",
    reason:
      "Translate requires neural translation models. Not feasible to replicate locally without bundling a large MT model.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lex",
    label: "Amazon Lex",
    category: "ml",
    description:
      "Conversational AI service for building chatbots and voice interfaces using the same technology as Alexa.",
    awsDocsUrl: "https://docs.aws.amazon.com/lexv2/latest/dg/",
    reason:
      "Lex requires dialogue management and NLU models. A configuration-only stub could be feasible in the future.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "personalize",
    label: "Amazon Personalize",
    category: "ml",
    description:
      "ML service for building real-time personalized recommendations, search, and content ranking.",
    awsDocsUrl: "https://docs.aws.amazon.com/personalize/latest/dg/",
    reason:
      "Personalize requires ML training infrastructure and a recommendation engine. Not feasible locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "forecast",
    label: "Amazon Forecast",
    category: "ml",
    description:
      "Time-series forecasting service using ML to generate accurate predictions from historical data.",
    awsDocsUrl: "https://docs.aws.amazon.com/forecast/latest/dg/",
    reason:
      "Forecast requires ML training infrastructure to build models from your historical data, then serves predictions from those trained models. Even a stub could only return placeholder numbers — any test validating prediction accuracy against a local emulation would be inherently misleading.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "kendra",
    label: "Amazon Kendra",
    category: "ml",
    description:
      "Intelligent enterprise search service powered by ML, enabling natural language queries across organizational content.",
    awsDocsUrl: "https://docs.aws.amazon.com/kendra/latest/dg/",
    reason:
      "Kendra requires an ML-powered index and content crawler. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "healthlake",
    label: "AWS HealthLake",
    category: "ml",
    description:
      "FHIR-compatible data store and analytics service for healthcare and life sciences data.",
    awsDocsUrl: "https://docs.aws.amazon.com/healthlake/latest/devguide/",
    reason:
      "HealthLake is a specialized healthcare data service with FHIR compliance requirements. Not a common local development target.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lookoutvision",
    label: "Amazon Lookout for Vision",
    category: "ml",
    description:
      "Computer vision service for detecting visual anomalies in products and processes using ML.",
    awsDocsUrl: "https://docs.aws.amazon.com/lookout-for-vision/latest/developer-guide/",
    reason:
      "Lookout for Vision trains custom CV anomaly-detection models on images of your own products. The entire value is the trained model output — without real CV training and inference infrastructure there is nothing useful to emulate.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lookoutmetrics",
    label: "Amazon Lookout for Metrics",
    category: "ml",
    description:
      "Anomaly detection service that uses ML to automatically find anomalies in business and operational metrics.",
    awsDocsUrl: "https://docs.aws.amazon.com/lookoutmetrics/latest/dev/",
    reason:
      "Lookout for Metrics requires ML-based anomaly detection models trained on metric data.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lookoutequipment",
    label: "Amazon Lookout for Equipment",
    category: "ml",
    description:
      "ML service for detecting abnormal equipment behavior and predicting failures in industrial machinery.",
    awsDocsUrl: "https://docs.aws.amazon.com/lookout-for-equipment/latest/ug/",
    reason:
      "Lookout for Equipment trains anomaly-detection models on historical sensor data from industrial machinery. It targets a narrow industrial IoT segment, requires real sensor telemetry to be useful, and has no API surface relevant to general application development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── IoT & Edge ─────────────────────────────────────────────────────────
  {
    id: "iot",
    label: "AWS IoT Core",
    category: "iot",
    description:
      "Managed cloud platform for connecting IoT devices, processing device data, and routing messages to AWS services.",
    awsDocsUrl: "https://docs.aws.amazon.com/iot/latest/developerguide/",
    reason:
      "IoT Core requires an MQTT broker and device registry infrastructure. A local MQTT broker is more appropriate for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "greengrass",
    label: "AWS IoT Greengrass",
    category: "iot",
    description:
      "Edge runtime that extends AWS cloud capabilities to local devices, enabling local compute and messaging.",
    awsDocsUrl: "https://docs.aws.amazon.com/greengrass/v2/developerguide/",
    reason:
      "Greengrass is an edge deployment runtime tied to specific hardware and the IoT device management infrastructure.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "iotanalytics",
    label: "AWS IoT Analytics",
    category: "iot",
    description:
      "Fully managed analytics service for IoT sensor data, with a pipeline to collect, process, and analyze device data.",
    awsDocsUrl: "https://docs.aws.amazon.com/iotanalytics/latest/userguide/",
    reason:
      "IoT Analytics pipelines data from IoT Core through configurable transformation stages before storing it for analysis. Without real device data flowing through IoT Core there is no data to process — and the analytics value is entirely absent in a local development context.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "iotevents",
    label: "AWS IoT Events",
    category: "iot",
    description:
      "Managed service for detecting and responding to events from IoT sensors and applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/iotevents/latest/developerguide/",
    reason:
      "IoT Events evaluates streams of IoT Core messages against detector models (state machines) and fires actions when conditions are met. Its utility is tightly coupled to live device data; the state machine primitives also overlap significantly with Step Functions, which Overcast already supports.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "iotsitewise",
    label: "AWS IoT SiteWise",
    category: "iot",
    description:
      "Managed service for collecting, storing, and analyzing industrial equipment data at scale.",
    awsDocsUrl: "https://docs.aws.amazon.com/iot-sitewise/latest/userguide/",
    reason:
      "IoT SiteWise is a specialized industrial IoT service with OPC UA and equipment modeling requirements.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "iottwinmaker",
    label: "AWS IoT TwinMaker",
    category: "iot",
    description:
      "Service for creating digital twins of real-world systems by integrating IoT data with 3D models.",
    awsDocsUrl: "https://docs.aws.amazon.com/iot-twinmaker/latest/guide/",
    reason:
      "IoT TwinMaker requires a 3D scene renderer and a live data source (typically IoT SiteWise) to populate the digital twin. Both the rendering infrastructure and the industrial data streams are absent in a local development environment.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "iotfleetwise",
    label: "AWS IoT FleetWise",
    category: "iot",
    description:
      "Managed service for collecting, transforming, and transferring vehicle data to the cloud.",
    awsDocsUrl: "https://docs.aws.amazon.com/iot-fleetwise/latest/developerguide/",
    reason:
      "IoT FleetWise is an automotive-specific telematics service for collecting in-vehicle CAN bus data from connected vehicles. It requires physical vehicle hardware or simulators with automotive protocols — there is no development workflow applicable outside the automotive OEM context.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Media Services ─────────────────────────────────────────────────────
  {
    id: "mediaconvert",
    label: "AWS Elemental MediaConvert",
    category: "media",
    description: "File-based video transcoding service for broadcast and multiscreen delivery.",
    awsDocsUrl: "https://docs.aws.amazon.com/mediaconvert/latest/ug/",
    reason:
      "MediaConvert requires codec infrastructure and GPU-accelerated transcoding pipelines. Not feasible to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "medialive",
    label: "AWS Elemental MediaLive",
    category: "media",
    description: "Broadcast-grade live video processing service for encoding live video streams.",
    awsDocsUrl: "https://docs.aws.amazon.com/medialive/latest/ug/",
    reason:
      "MediaLive requires real-time video encoding infrastructure. Not feasible to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "mediapackage",
    label: "AWS Elemental MediaPackage",
    category: "media",
    description:
      "Video origination and packaging service for preparing live and on-demand video for internet delivery.",
    awsDocsUrl: "https://docs.aws.amazon.com/mediapackage/latest/ug/",
    reason:
      "MediaPackage requires streaming media infrastructure. Not feasible to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "mediastore",
    label: "AWS Elemental MediaStore",
    category: "media",
    description: "Media-optimized storage service providing low-latency storage for live video.",
    awsDocsUrl: "https://docs.aws.amazon.com/mediastore/latest/ug/",
    reason:
      "MediaStore is a media-optimized storage system. Use S3 locally for object storage needs.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "mediatailor",
    label: "AWS Elemental MediaTailor",
    category: "media",
    description:
      "Personalized ad insertion service that replaces content in live and on-demand video streams.",
    awsDocsUrl: "https://docs.aws.amazon.com/mediatailor/latest/ug/",
    reason:
      "MediaTailor performs server-side ad insertion by splicing ad segments into live HLS or DASH video streams. It requires an active streaming origin and an ad decision server (ADS) — without a real video delivery pipeline there is nothing to personalise.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "ivs",
    label: "Amazon IVS",
    category: "media",
    description: "Managed live streaming solution for building interactive video experiences.",
    awsDocsUrl: "https://docs.aws.amazon.com/ivs/latest/userguide/",
    reason:
      "IVS requires video streaming infrastructure and CDN integration. Not applicable for local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Migration & Transfer ───────────────────────────────────────────────
  {
    id: "dms",
    label: "AWS Database Migration Service",
    category: "migration",
    description:
      "Service for migrating databases to AWS with minimal downtime, supporting homogeneous and heterogeneous migrations.",
    awsDocsUrl: "https://docs.aws.amazon.com/dms/latest/userguide/",
    reason:
      "DMS requires connectivity to source and target database systems. The value proposition is specific to migration scenarios, not ongoing local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "mgn",
    label: "AWS Application Migration Service",
    category: "migration",
    description:
      "Lift-and-shift migration service for automating server replication and cutover to AWS.",
    awsDocsUrl: "https://docs.aws.amazon.com/mgn/latest/ug/",
    reason:
      "Application Migration Service is a one-time server migration tool: you install an agent on source servers, it replicates disks to AWS, then orchestrates a cutover. This is a migration operation rather than an ongoing API that applications call at runtime — there is no development workflow that benefits from a local emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "transfer",
    label: "AWS Transfer Family",
    category: "migration",
    description:
      "Managed SFTP, FTPS, and FTP service for directly transferring files into and out of S3.",
    awsDocsUrl: "https://docs.aws.amazon.com/transfer/latest/userguide/",
    reason:
      "Transfer Family requires a managed SFTP/FTP server. A local stub could be feasible in the future given Overcast's S3 support.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "datasync",
    label: "AWS DataSync",
    category: "migration",
    description:
      "Online data transfer service for moving data between on-premises and AWS storage.",
    awsDocsUrl: "https://docs.aws.amazon.com/datasync/latest/userguide/",
    reason:
      "DataSync transfers data between on-premises storage systems (NFS, SMB, HDFS) and AWS using an agent VM deployed in your data center. It is a data-movement pipeline rather than an API that applications call at runtime — there is no SDK integration pattern that benefits from local emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "snowball",
    label: "AWS Snow Family",
    category: "migration",
    description:
      "Physical edge computing and data transfer devices (Snowcone, Snowball, Snowmobile) for large-scale data transport.",
    awsDocsUrl: "https://docs.aws.amazon.com/snowball/latest/developer-guide/",
    reason:
      "Snow Family devices (Snowcone, Snowball Edge, Snowmobile) are physical hardware appliances for moving large datasets where internet transfer is impractical. The management API models a physical logistics and shipping workflow — the data-at-rest transfer value cannot be reproduced without the actual devices.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Developer Tools ────────────────────────────────────────────────────
  {
    id: "codebuild",
    label: "AWS CodeBuild",
    category: "devtools",
    description:
      "Fully managed continuous integration service that compiles code, runs tests, and produces deployable packages.",
    awsDocsUrl: "https://docs.aws.amazon.com/codebuild/latest/userguide/",
    reason:
      "CodeBuild requires managed build agent infrastructure. Use a local CI runner (GitHub Actions local runner, etc.) for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "codedeploy",
    label: "AWS CodeDeploy",
    category: "devtools",
    description:
      "Deployment service that automates code deployments to EC2 instances, Lambda, or on-premises servers.",
    awsDocsUrl: "https://docs.aws.amazon.com/codedeploy/latest/userguide/",
    reason:
      "CodeDeploy requires deployment agent infrastructure and integration with target compute environments.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "codepipeline",
    label: "AWS CodePipeline",
    category: "devtools",
    description: "Fully managed continuous delivery service for automating release pipelines.",
    awsDocsUrl: "https://docs.aws.amazon.com/codepipeline/latest/userguide/",
    reason:
      "CodePipeline orchestrates build and deploy stages that require real compute infrastructure.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "codeartifact",
    label: "AWS CodeArtifact",
    category: "devtools",
    description:
      "Managed artifact repository service for storing, publishing, and sharing packages (npm, Maven, PyPI, etc.).",
    awsDocsUrl: "https://docs.aws.amazon.com/codeartifact/latest/ug/",
    reason:
      "CodeArtifact is a package registry. Use a local Nexus, Verdaccio, or similar tool for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "codecatalyst",
    label: "Amazon CodeCatalyst",
    category: "devtools",
    description:
      "Unified development collaboration platform with project management, source control, and CI/CD workflows.",
    awsDocsUrl: "https://docs.aws.amazon.com/codecatalyst/latest/userguide/",
    reason:
      "CodeCatalyst is a SaaS collaboration platform. There is no local development equivalent.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "fis",
    label: "AWS Fault Injection Service",
    category: "devtools",
    description:
      "Managed fault injection service for running chaos engineering experiments on AWS workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/fis/latest/userguide/",
    reason:
      "FIS could be a useful local testing tool. A stub that records experiment definitions could be added in the future.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "xray",
    label: "AWS X-Ray",
    category: "devtools",
    description:
      "Distributed tracing service for analyzing and debugging production and distributed applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/xray/latest/devguide/",
    reason:
      "X-Ray requires a trace aggregation daemon and storage backend. Use Jaeger or OpenTelemetry locally for distributed tracing.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Management & Governance ────────────────────────────────────────────
  {
    id: "cloudtrail",
    label: "AWS CloudTrail",
    category: "management",
    description:
      "Service that records API activity and account events for governance, compliance, and operational auditing.",
    awsDocsUrl: "https://docs.aws.amazon.com/awscloudtrail/latest/userguide/",
    reason:
      "CloudTrail records API calls across all services. A local implementation could capture and expose Overcast API activity.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "config",
    label: "AWS Config",
    category: "management",
    description:
      "Service for continuously monitoring and recording AWS resource configurations and evaluating compliance.",
    awsDocsUrl: "https://docs.aws.amazon.com/config/latest/developerguide/",
    reason:
      "Config continuously records the configuration state of AWS resources and evaluates them against compliance rules. Faithfully emulating it would require tracking resource changes across every Overcast service and running a rules evaluation engine — and compliance auditing is a production governance activity with no practical development-time analogue.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "organizations",
    label: "AWS Organizations",
    category: "management",
    description:
      "Account management service for governing multiple AWS accounts, applying policies, and organizing them into organizational units.",
    awsDocsUrl: "https://docs.aws.amazon.com/organizations/latest/userguide/",
    reason:
      "Organizations manages a hierarchy of AWS accounts with SCPs, consolidated billing, and delegated administrators. All its semantics depend on real AWS account isolation boundaries — Overcast has no multi-account model, so the shared-root and policy primitives have nothing to operate on.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "controltower",
    label: "AWS Control Tower",
    category: "management",
    description:
      "Service for setting up and governing a secure, compliant multi-account AWS environment.",
    awsDocsUrl: "https://docs.aws.amazon.com/controltower/latest/userguide/",
    reason:
      "Control Tower orchestrates Organizations, SCPs, CloudFormation StackSets, and IAM Identity Center to provision new accounts with governance guardrails. It is an infrastructure-bootstrapping service for teams managing many AWS accounts — there is no application development workflow that requires a local Control Tower emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "servicecatalog",
    label: "AWS Service Catalog",
    category: "management",
    description:
      "Service for creating and managing approved catalogs of IT services for use in AWS, built on CloudFormation.",
    awsDocsUrl: "https://docs.aws.amazon.com/servicecatalog/latest/adminguide/",
    reason:
      "Service Catalog depends on CloudFormation and organizational policies. Not a common local development target.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "licensemanager",
    label: "AWS License Manager",
    category: "management",
    description:
      "Service for managing software licenses from vendors like Microsoft, Oracle, and IBM in AWS and on-premises.",
    awsDocsUrl: "https://docs.aws.amazon.com/license-manager/latest/userguide/",
    reason:
      "License Manager is a licensing governance service. Not applicable for local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "proton",
    label: "AWS Proton",
    category: "management",
    description:
      "Platform engineering service for defining and deploying infrastructure templates for container and serverless applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/proton/latest/userguide/",
    reason:
      "Proton orchestrates CloudFormation and Terraform templates by platform teams. Depends heavily on CloudFormation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "wellarchitected",
    label: "AWS Well-Architected Tool",
    category: "management",
    description:
      "Tool for reviewing the state of applications and workloads against AWS architectural best practices.",
    awsDocsUrl: "https://docs.aws.amazon.com/wellarchitected/latest/userguide/",
    reason:
      "The Well-Architected Tool is an advisory/review service. There is no meaningful local equivalent.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "resilience-hub",
    label: "AWS Resilience Hub",
    category: "management",
    description:
      "Service for assessing, validating, and tracking application resiliency against disruption targets.",
    awsDocsUrl: "https://docs.aws.amazon.com/resilience-hub/latest/userguide/",
    reason:
      "Resilience Hub analyzes live AWS infrastructure for resilience gaps. Not applicable for local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "resource-groups",
    label: "AWS Resource Groups",
    category: "management",
    description:
      "Service for grouping AWS resources by tags for bulk management, monitoring, and automation.",
    awsDocsUrl: "https://docs.aws.amazon.com/ARG/latest/userguide/",
    reason:
      "Resource Groups is a tag-based resource grouping service. A basic implementation could be added in the future.",
    tier: "unsupported",
    goalTier: "stub",
  },
  // ── Security & Identity (unsupported) ──────────────────────────────────
  {
    id: "cloudhsm",
    label: "AWS CloudHSM",
    category: "security",
    description:
      "Cloud-based hardware security module for cryptographic key storage and operations.",
    awsDocsUrl: "https://docs.aws.amazon.com/cloudhsm/latest/userguide/",
    reason:
      "CloudHSM is a hardware security appliance. There is no software emulation that preserves its security properties.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "acm",
    label: "AWS Certificate Manager",
    category: "security",
    description:
      "Service for provisioning, managing, and deploying TLS/SSL certificates for AWS services.",
    awsDocsUrl: "https://docs.aws.amazon.com/acm/latest/userguide/",
    reason:
      "ACM could be partially emulated for local HTTPS development, but certificate issuance requires a CA.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "acm-pca",
    label: "AWS Private CA",
    category: "security",
    description:
      "Managed private certificate authority for issuing TLS certificates for internal resources.",
    awsDocsUrl: "https://docs.aws.amazon.com/privateca/latest/userguide/",
    reason:
      "Private CA requires a cryptographically secure CA infrastructure. Use an open-source CA (e.g. Step CA) locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "guardduty",
    label: "Amazon GuardDuty",
    category: "security",
    description:
      "Continuous threat detection service using ML and threat intelligence to identify malicious activity.",
    awsDocsUrl: "https://docs.aws.amazon.com/guardduty/latest/ug/",
    reason:
      "GuardDuty analyses VPC flow logs, DNS logs, and CloudTrail events using ML and threat intelligence to detect malicious activity. Without real account traffic and threat intelligence feeds there is nothing to analyse — threat detection is also a production security concern, not a development-time one.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "inspector",
    label: "Amazon Inspector",
    category: "security",
    description:
      "Automated vulnerability management service that continuously scans EC2, Lambda, and container workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/inspector/latest/user/",
    reason:
      "Inspector requires a vulnerability database and active scanning infrastructure. Not applicable for local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "macie",
    label: "Amazon Macie",
    category: "security",
    description:
      "ML-powered data security service that discovers and protects sensitive data in S3.",
    awsDocsUrl: "https://docs.aws.amazon.com/macie/latest/user/",
    reason:
      "Macie uses ML to classify S3 data for sensitive content. Not practical to replicate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "securityhub",
    label: "AWS Security Hub",
    category: "security",
    description:
      "Central security findings hub that aggregates alerts from GuardDuty, Inspector, Macie, and other security services.",
    awsDocsUrl: "https://docs.aws.amazon.com/securityhub/latest/userguide/",
    reason:
      "Security Hub aggregates findings from other security services. Without those services, there are no findings to aggregate.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "detective",
    label: "Amazon Detective",
    category: "security",
    description:
      "Security investigation service that automatically collects log data and uses ML to enable faster root cause analysis.",
    awsDocsUrl: "https://docs.aws.amazon.com/detective/latest/userguide/",
    reason:
      "Detective is an ML-powered forensics service that depends on CloudTrail and GuardDuty data.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "ds",
    label: "AWS Directory Service",
    category: "security",
    description:
      "Managed Microsoft Active Directory service for using directory-aware workloads and AWS services.",
    awsDocsUrl: "https://docs.aws.amazon.com/directoryservice/latest/admin-guide/",
    reason:
      "Directory Service requires a managed Active Directory infrastructure. Use a local LDAP server for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "sso",
    label: "AWS IAM Identity Center",
    category: "security",
    description:
      "Centralized SSO service for managing workforce access to multiple AWS accounts and applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/singlesignon/latest/userguide/",
    reason:
      "IAM Identity Center provides workforce SSO across multiple AWS accounts and integrated SaaS applications. Its value depends on real account isolation and external identity providers — Overcast emulates a single account with no authentication enforcement, so the SSO layer has nothing meaningful to mediate.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "ram",
    label: "AWS Resource Access Manager",
    category: "security",
    description: "Service for sharing AWS resources across accounts and organizations.",
    awsDocsUrl: "https://docs.aws.amazon.com/ram/latest/userguide/",
    reason:
      "RAM enables cross-account sharing of resources like VPC subnets, Transit Gateways, and Resolver rules. All sharing semantics depend on real AWS account boundaries — Overcast is a single-account emulator with no account-isolation model to share across.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "verifiedpermissions",
    label: "Amazon Verified Permissions",
    category: "security",
    description:
      "Fine-grained authorization service using the Cedar policy language for application-level access control.",
    awsDocsUrl: "https://docs.aws.amazon.com/verifiedpermissions/latest/userguide/",
    reason:
      "Verified Permissions could be feasibly emulated using the open-source Cedar policy engine.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "auditmanager",
    label: "AWS Audit Manager",
    category: "security",
    description:
      "Service for continuously auditing your AWS usage to simplify risk and compliance assessment.",
    awsDocsUrl: "https://docs.aws.amazon.com/audit-manager/latest/userguide/",
    reason:
      "Audit Manager automatically collects evidence from CloudTrail, Config, and Security Hub to generate compliance reports against frameworks like PCI-DSS and SOC 2. Without those live data sources there is no evidence to collect — and compliance auditing is a production governance activity, not a development-time concern.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "securitylake",
    label: "Amazon Security Lake",
    category: "security",
    description:
      "Centralizes security data into a purpose-built data lake using the Open Cybersecurity Schema Framework.",
    awsDocsUrl: "https://docs.aws.amazon.com/security-lake/latest/userguide/",
    reason:
      "Security Lake aggregates data from security tools and AWS services into a managed OCSF data lake.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Networking (unsupported) ───────────────────────────────────────────
  {
    id: "route53",
    label: "Amazon Route 53",
    category: "networking",
    description:
      "Scalable DNS web service with domain registration, health checking, and traffic routing capabilities.",
    awsDocsUrl: "https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/",
    reason:
      "Route 53 DNS emulation could be useful locally. A stub that accepts zone and record configuration is feasible.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "directconnect",
    label: "AWS Direct Connect",
    category: "networking",
    description: "Dedicated network connection between on-premises infrastructure and AWS.",
    awsDocsUrl: "https://docs.aws.amazon.com/directconnect/latest/UserGuide/",
    reason:
      "Direct Connect is a physical network infrastructure service. There is no software emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "globalaccelerator",
    label: "AWS Global Accelerator",
    category: "networking",
    description:
      "Networking service that improves availability and performance by routing traffic through the AWS global network.",
    awsDocsUrl: "https://docs.aws.amazon.com/global-accelerator/latest/dg/",
    reason:
      "Global Accelerator routes traffic through the AWS anycast network. There is no local equivalent.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "transitgateway",
    label: "AWS Transit Gateway",
    category: "networking",
    description: "Network transit hub for interconnecting VPCs and on-premises networks at scale.",
    awsDocsUrl: "https://docs.aws.amazon.com/vpc/latest/tgw/",
    reason:
      "Transit Gateway is a regional routing hub for interconnecting VPCs and on-premises networks, requiring real AWS network fabric to route actual traffic. Even with partial EC2/VPC support, routing rules only make sense when real network flows are present — a configuration-only stub would give false confidence in routing topologies.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "elasticloadbalancing",
    label: "Elastic Load Balancing",
    category: "networking",
    description:
      "Distributes incoming application traffic across multiple targets (EC2, Lambda, containers) using ALB, NLB, or CLB.",
    awsDocsUrl: "https://docs.aws.amazon.com/elasticloadbalancing/latest/userguide/",
    reason:
      "ELB could be emulated as a request routing stub, but the value without real EC2 or container targets is limited.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "network-firewall",
    label: "AWS Network Firewall",
    category: "networking",
    description: "Managed stateful firewall for filtering network traffic at the VPC level.",
    awsDocsUrl: "https://docs.aws.amazon.com/network-firewall/latest/developerguide/",
    reason:
      "Network Firewall performs stateful and stateless deep packet inspection at the VPC boundary using kernel-level networking. Its value is entirely in enforcing policies on real network traffic — locally there is no traffic to inspect, and accept/reject behaviour cannot be meaningfully simulated.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "vpc-lattice",
    label: "Amazon VPC Lattice",
    category: "networking",
    description:
      "Service-to-service connectivity and HTTP routing at the application layer within VPCs.",
    awsDocsUrl: "https://docs.aws.amazon.com/vpc-lattice/latest/ug/",
    reason:
      "VPC Lattice provides application-layer service-to-service connectivity with policy-based access control between VPCs. Its routing rules only take effect when real AWS network fabric is present — locally, applications can communicate directly without any mesh machinery, making an emulation misleading rather than helpful.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Database (unsupported) ─────────────────────────────────────────────
  {
    id: "neptune",
    label: "Amazon Neptune",
    category: "storage",
    description:
      "Fully managed graph database service supporting Property Graph (Gremlin/openCypher) and RDF (SPARQL) models.",
    awsDocsUrl: "https://docs.aws.amazon.com/neptune/latest/userguide/",
    reason:
      "Neptune requires a graph database engine. Use a local Neo4j or Apache Jena container for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "docdb",
    label: "Amazon DocumentDB",
    category: "storage",
    description: "MongoDB-compatible managed document database service.",
    awsDocsUrl: "https://docs.aws.amazon.com/documentdb/latest/developerguide/",
    reason:
      "DocumentDB requires a MongoDB-compatible document database engine. Use a local MongoDB container for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "timestream",
    label: "Amazon Timestream",
    category: "storage",
    description:
      "Fully managed time-series database for IoT and operational workloads, with adaptive query processing.",
    awsDocsUrl: "https://docs.aws.amazon.com/timestream/latest/developerguide/",
    reason:
      "Timestream requires a specialized time-series storage engine with adaptive compression. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "qldb",
    label: "Amazon QLDB",
    category: "storage",
    description:
      "Fully managed ledger database with a cryptographically verifiable transaction log.",
    awsDocsUrl: "https://docs.aws.amazon.com/qldb/latest/developerguide/",
    reason: "QLDB requires a cryptographic ledger implementation with immutable journal semantics.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "keyspaces",
    label: "Amazon Keyspaces",
    category: "storage",
    description: "Managed Apache Cassandra-compatible database service for Cassandra workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/keyspaces/latest/devguide/",
    reason:
      "Keyspaces requires a Cassandra-compatible database engine. Use a local Cassandra container for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "memorydb",
    label: "Amazon MemoryDB for Redis",
    category: "storage",
    description:
      "Redis-compatible durable in-memory database service for ultra-fast data access with Multi-AZ durability.",
    awsDocsUrl: "https://docs.aws.amazon.com/memorydb/latest/devguide/",
    reason: "MemoryDB is a durable Redis variant. Use a local Redis container for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Storage (unsupported) ──────────────────────────────────────────────
  {
    id: "fsx",
    label: "Amazon FSx",
    category: "storage",
    description:
      "Fully managed file systems for Windows (DFS/SMB), Lustre, NetApp ONTAP, and OpenZFS workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/fsx/latest/WindowsGuide/",
    reason:
      "FSx provides managed versions of specialized file systems. Use local file system tools for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "backup",
    label: "AWS Backup",
    category: "storage",
    description:
      "Centralized backup service for automating data protection across AWS services and hybrid workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/aws-backup/latest/devguide/",
    reason:
      "AWS Backup orchestrates backups across multiple services. Depends on those services being fully implemented.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "storagegateway",
    label: "AWS Storage Gateway",
    category: "storage",
    description:
      "Hybrid cloud storage service connecting on-premises environments to AWS cloud storage.",
    awsDocsUrl: "https://docs.aws.amazon.com/storagegateway/latest/userguide/",
    reason:
      "Storage Gateway requires an on-premises virtual appliance. Not applicable for local-only development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Compute (unsupported) ──────────────────────────────────────────────
  {
    id: "apprunner",
    label: "AWS App Runner",
    category: "compute",
    description:
      "Fully managed service for building and running containerized web applications without managing infrastructure.",
    awsDocsUrl: "https://docs.aws.amazon.com/apprunner/latest/dg/",
    reason:
      "App Runner is a managed container runtime service. Use ECS or Docker locally for container development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "batch",
    label: "AWS Batch",
    category: "compute",
    description:
      "Managed batch processing service for running hundreds of thousands of batch computing jobs on EC2 and Fargate.",
    awsDocsUrl: "https://docs.aws.amazon.com/batch/latest/userguide/",
    reason:
      "Batch requires a job scheduler and managed compute fleet. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "elasticbeanstalk",
    label: "AWS Elastic Beanstalk",
    category: "compute",
    description:
      "PaaS service for deploying and managing web applications without worrying about provisioning infrastructure.",
    awsDocsUrl: "https://docs.aws.amazon.com/elasticbeanstalk/latest/dg/",
    reason:
      "Elastic Beanstalk orchestrates EC2, RDS, and load balancers. Not practical without deeper infrastructure emulation.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "lightsail",
    label: "Amazon Lightsail",
    category: "compute",
    description:
      "Simplified virtual servers (VPS), databases, storage, and networking for smaller workloads.",
    awsDocsUrl: "https://docs.aws.amazon.com/lightsail/latest/userguide/",
    reason:
      "Lightsail is a simplified VPS product. Not a common CDK or SDK target; use Docker or EC2 locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  {
    id: "xray-monitoring",
    label: "AWS X-Ray",
    category: "monitoring",
    description:
      "Distributed tracing service for analyzing and debugging production and distributed applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/xray/latest/devguide/",
    reason:
      "X-Ray requires a trace collection daemon and storage backend. Use Jaeger or OpenTelemetry locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "evidently",
    label: "CloudWatch Evidently",
    category: "monitoring",
    description:
      "Feature flagging and A/B testing service for safely launching and validating new application features.",
    awsDocsUrl: "https://docs.aws.amazon.com/cloudwatchevidently/latest/userguide/",
    reason:
      "CloudWatch Evidently is a feature flag service. Overcast could offer a stub in the future.",
    tier: "unsupported",
    goalTier: "stub",
  },
  {
    id: "rum",
    label: "CloudWatch RUM",
    category: "monitoring",
    description:
      "Real user monitoring service for collecting client-side performance data from web applications.",
    awsDocsUrl:
      "https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-RUM.html",
    reason:
      "CloudWatch RUM collects browser-side telemetry. Not applicable for server-side local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "trustedadvisor",
    label: "AWS Trusted Advisor",
    category: "monitoring",
    description:
      "Real-time best practices recommendations for cost optimization, security, fault tolerance, and performance.",
    awsDocsUrl: "https://docs.aws.amazon.com/awssupport/latest/user/trusted-advisor.html",
    reason:
      "Trusted Advisor inspects your live AWS account and surfaces recommendations based on actual resource usage and spending patterns. Without real account data and billing metrics there are no recommendations to generate — it is inherently a production-time cost-optimisation and governance tool.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Business Applications ──────────────────────────────────────────────
  {
    id: "connect",
    label: "Amazon Connect",
    category: "business",
    description:
      "Cloud-based contact center service for customer service with omnichannel communications.",
    awsDocsUrl: "https://docs.aws.amazon.com/connect/latest/adminguide/",
    reason:
      "Amazon Connect is a full contact-centre platform with phone number provisioning, IVR flows, real-time agent routing, and call recording. Its core functionality depends on live telephony carrier integration — there is no meaningful subset that can be emulated without actual phone infrastructure.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "pinpoint",
    label: "Amazon Pinpoint",
    category: "business",
    description:
      "Multi-channel marketing communications platform for sending email, SMS, push notifications, and voice messages.",
    awsDocsUrl: "https://docs.aws.amazon.com/pinpoint/latest/userguide/",
    reason:
      "Pinpoint requires delivery infrastructure for external channels. A simplified local mock could be added in the future.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "workspaces",
    label: "Amazon WorkSpaces",
    category: "business",
    description:
      "Managed virtual desktop infrastructure (VDI) for provisioning cloud-based desktops for end users.",
    awsDocsUrl: "https://docs.aws.amazon.com/workspaces/latest/adminguide/",
    reason:
      "WorkSpaces provisions persistent Windows or Linux desktop VMs for end users and streams them over PCoIP or WSP. It is an IT provisioning service — applications do not call WorkSpaces APIs at runtime, so there is no SDK integration surface worth emulating locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "workmail",
    label: "Amazon WorkMail",
    category: "business",
    description: "Managed business email and calendaring service.",
    awsDocsUrl: "https://docs.aws.amazon.com/workmail/latest/adminguide/",
    reason:
      "WorkMail is an organisational email and calendar platform for end users. For programmatic email sending from applications, SES (which Overcast supports) is the correct service — WorkMail has no SDK integration surface relevant to application development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "appstream",
    label: "Amazon AppStream 2.0",
    category: "business",
    description:
      "Managed application streaming service for delivering desktop applications via a browser.",
    awsDocsUrl: "https://docs.aws.amazon.com/appstream2/latest/developerguide/",
    reason:
      "AppStream streams graphical desktop applications from AWS VM instances to a browser. Like WorkSpaces, it is an end-user publishing platform — applications do not call AppStream APIs at runtime, so there is no development-time emulation target.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "location",
    label: "Amazon Location Service",
    category: "business",
    description:
      "Location-based services for adding maps, geofencing, tracking, and routing to applications.",
    awsDocsUrl: "https://docs.aws.amazon.com/location/latest/developerguide/",
    reason:
      "Location Service requires map data, geocoding databases, and routing engines. Not practical to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "b2bi",
    label: "AWS B2B Data Interchange",
    category: "business",
    description:
      "Service for modernizing EDI (Electronic Data Interchange) workflows with managed B2B data transformation.",
    awsDocsUrl: "https://docs.aws.amazon.com/b2bi/latest/userguide/",
    reason:
      "B2B Data Interchange is a specialized EDI/B2B integration service. Not a common local development target.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Billing & Cost Management ──────────────────────────────────────────
  {
    id: "ce",
    label: "AWS Cost Explorer",
    category: "billing",
    description:
      "Cost analysis service for visualizing, understanding, and managing AWS costs and usage over time.",
    awsDocsUrl: "https://docs.aws.amazon.com/cost-management/latest/userguide/ce-what-is.html",
    reason:
      "Cost Explorer is a billing analytics service. Overcast has no real billing metering, so there are no costs to analyze.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "budgets",
    label: "AWS Budgets",
    category: "billing",
    description:
      "Service for setting custom cost and usage alerts and automation when thresholds are exceeded.",
    awsDocsUrl:
      "https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html",
    reason: "AWS Budgets is a billing management service. Overcast has no real billing metering.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Game Tech ──────────────────────────────────────────────────────────
  {
    id: "gamelift",
    label: "Amazon GameLift",
    category: "gametech",
    description:
      "Managed game server hosting for deploying, operating, and scaling dedicated game servers.",
    awsDocsUrl: "https://docs.aws.amazon.com/gamelift/latest/developerguide/",
    reason:
      "GameLift manages fleets of game server processes running your own compiled server binary, and provides matchmaking and session placement. Game servers can run locally as plain processes during development — the GameLift control plane only adds value when deploying to AWS, so a local emulation offers little benefit.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Robotics ───────────────────────────────────────────────────────────
  {
    id: "robomaker",
    label: "AWS RoboMaker",
    category: "robotics",
    description:
      "Service for developing, testing, and deploying robotics applications at scale using ROS.",
    awsDocsUrl: "https://docs.aws.amazon.com/robomaker/latest/dg/",
    reason:
      "RoboMaker requires a robotics simulation infrastructure with ROS support. Not applicable for general local development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Quantum Computing ──────────────────────────────────────────────────
  {
    id: "braket",
    label: "Amazon Braket",
    category: "quantum",
    description:
      "Managed quantum computing service for experimenting with quantum algorithms on quantum hardware simulators.",
    awsDocsUrl: "https://docs.aws.amazon.com/braket/latest/developerguide/",
    reason:
      "Braket requires quantum hardware or specialized simulators. Not feasible to emulate locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Satellite ──────────────────────────────────────────────────────────
  {
    id: "groundstation",
    label: "AWS Ground Station",
    category: "satellite",
    description:
      "Managed ground station service for communicating with and processing data from satellites.",
    awsDocsUrl: "https://docs.aws.amazon.com/ground-station/latest/ug/",
    reason:
      "Ground Station requires satellite dish infrastructure. There is no software emulation possible.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Front End & Mobile ──────────────────────────────────────────────────
  {
    id: "amplify",
    label: "AWS Amplify",
    category: "devtools",
    description:
      "Full-stack platform for building, deploying, and hosting web and mobile applications with backend integration.",
    awsDocsUrl: "https://docs.aws.amazon.com/amplify/latest/userguide/",
    reason:
      "Amplify is a managed hosting and CI/CD platform. Use Vite, Next.js, or similar tooling locally.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
  {
    id: "devicefarm",
    label: "AWS Device Farm",
    category: "devtools",
    description:
      "App testing service for testing Android, iOS, and web apps on real physical devices in the cloud.",
    awsDocsUrl: "https://docs.aws.amazon.com/devicefarm/latest/developerguide/",
    reason:
      "Device Farm runs test suites against real Android and iOS hardware in AWS's device lab. The key value — catching device-specific bugs that software emulators miss — cannot be reproduced without physical devices. For local testing, standard platform emulators (Android Studio AVD, Xcode Simulator) are the appropriate substitutes.",
    tier: "unsupported",
    goalTier: "unsupported",
  },

  // ── Managed Blockchain ─────────────────────────────────────────────────
  {
    id: "managedblockchain",
    label: "Amazon Managed Blockchain",
    category: "management",
    description:
      "Managed blockchain network service for creating and managing scalable blockchain networks using Hyperledger Fabric or Ethereum.",
    awsDocsUrl: "https://docs.aws.amazon.com/managed-blockchain/latest/hyperledger-fabric-dev/",
    reason:
      "Managed Blockchain requires distributed ledger node infrastructure. Use a local Hyperledger Fabric setup for development.",
    tier: "unsupported",
    goalTier: "unsupported",
  },
]

/** O(1) lookup of a CatalogEntry by its `id` field. */
export const CATALOG_BY_ID: Record<string, CatalogEntry | undefined> = Object.fromEntries(
  CATALOG.map((e) => [e.id, e]),
)
