// Iconography here is a trademark rule, not a taste: every icon is a lucide (or original) glyph
// and every colour resolves through the `--cat-*` ramp. Never AWS Architecture Icons, AWS logos,
// or lookalikes — AWS's fair-use clause is plain text only and imitating its product icons is
// prohibited. See https://brand.overcast.sh/brand-guidelines.html#third-party-marks
/**
 * service-registry — single source of truth for all navigable AWS services.
 *
 * Every icon, colour slot, minimap letter, route path, nav category, and
 * description lives here. The sidebar, dashboard, global search, topology
 * map, and docs button all derive their values from this registry.
 *
 * When adding a new service:
 *   1. Add an entry to SERVICES below (visual identity + routing fields).
 *   2. That's it — sidebar, dashboard card, global search, and topology all
 *      derive from here automatically. See the field docs on ServiceEntry for
 *      which fields control which surfaces.
 */

import {
  Archive,
  MessagesSquare,
  Database,
  DatabaseZap,
  Bell,
  Zap,
  ScrollText,
  Boxes,
  Cpu,
  Mail,
  Cable,
  Waves,
  Radio,
  KeyRound,
  Users,
  UserCheck,
  Key,
  ShieldAlert,
  ShieldCheck,
  PlugZap,
  Globe,
  Braces,
  Layers,
  Activity,
  Workflow,
  Waypoints,
  SlidersHorizontal,
  Fingerprint,
  Network,
  HardDrive,
  Gauge,
  DatabaseSearch,
  LibraryBig,
  Table2,
  type LucideIcon,
} from "lucide-react"

// ── Category types ─────────────────────────────────────────────────────────

export type ServiceCategory =
  "storage" | "analytics" | "compute" | "messaging" | "security" | "networking" | "monitoring"

export const CATEGORY_LABELS: Record<ServiceCategory, string> = {
  storage: "Storage & Database",
  analytics: "Analytics",
  compute: "Compute",
  messaging: "Messaging",
  security: "Security & Identity",
  networking: "Networking & APIs",
  monitoring: "Monitoring",
}

export const CATEGORY_ORDER: ServiceCategory[] = [
  "storage",
  "analytics",
  "compute",
  "messaging",
  "security",
  "networking",
  "monitoring",
]

export interface SubNavItem {
  to: string
  label: string
}

export interface SubNavGroup {
  group: string
  items: SubNavItem[]
}

/** A child entry is either a direct link or a labelled group of links. */
export type SubNavChild = SubNavItem | SubNavGroup

// ── ServiceEntry ───────────────────────────────────────────────────────────

export interface ServiceEntry {
  // ── Visual identity ──────────────────────────────────────────────────────
  /** Human-readable label (e.g. "S3", "DynamoDB"). */
  label: string
  /** Lucide icon component. */
  icon: LucideIcon
  /**
   * Categorical-ramp text colour class (e.g. "text-cat-2"). Always a `cat-*`
   * slot — never a raw Tailwind hue, which would be theme-blind. See the slot
   * assignment note above SERVICES.
   */
  color: string
  /** Tint of the same slot, paired with the color (e.g. "bg-cat-2/10"). */
  bg: string
  /** Hairline of the same slot, paired with the color (e.g. "border-cat-2/30"). */
  border: string
  /**
   * The same slot as a raw CSS colour value (`var(--cat-2)`), for SVG
   * presentation attributes and inline styles — the minimap dots, the map's
   * node circles and the sweep animation, none of which can take a class.
   */
  css: string
  /** Single character shown in the minimap node pill. */
  letter: string

  // ── Routing & surfaces ───────────────────────────────────────────────────
  /**
   * Primary route path. Omit for entries that exist only for visual identity
   * on the topology map (vpc, igw, etc.).
   */
  to?: string
  /**
   * Nav sidebar category. Required when this service appears in the sidebar.
   */
  category?: ServiceCategory
  /**
   * Brief description shown in the sidebar nav and global search results.
   * Also used as the dashboard card description when dashboardDescription is absent.
   */
  description?: string
  /**
   * Longer, action-oriented description for the dashboard card.
   * Falls back to description when absent.
   */
  dashboardDescription?: string
  /**
   * Dashboard card label when it differs from label (e.g. "EC2 / VPC" vs "EC2").
   * Falls back to label.
   */
  dashboardLabel?: string
  /**
   * Filename stem in docs/services/<docKey>.md. Enables the ServiceDocsButton
   * on the dashboard card and service home pages.
   */
  docKey?: string
  /** Sidebar sub-navigation items. May be flat links or labelled groups. */
  children?: SubNavChild[]
  /**
   * Show in the sidebar nav.
   * Set to false for dashboard-only services (kms, ssm, sts, etc.).
   * @default true - when {@see ServiceEntry.to} + {@see ServiceEntry.category} are set, otherwise false
   */
  nav?: boolean
  /**
   * Show on the dashboard.
   * Set to false for stub/info-only services (for example, Shield) that don't warrant a card.
   * @default true - when {@see ServiceEntry.to} is set, otherwise false
   */
  dashboardCard?: boolean
  /**
   * Whether users can favourite/pin this service in the sidebar.
   * @default true - when {@see ServiceEntry.to} is set, otherwise false
   */
  favouritable?: boolean
}

// ── Registry ───────────────────────────────────────────────────────────────

/**
 * Ramp-slot assignment (docs/plans/palette-categorical-tokens.md, decisions 1-4).
 *
 * Every `color`/`bg`/`border`/`css` below resolves through the ten-slot
 * categorical ramp declared in `styles/global.css`, so each one has a distinct
 * light and dark value. The slot is **the ramp slot whose OKLCH hue is nearest
 * the colour this service already had** — nothing else. That rule is
 * deterministic, order-independent, and moves no service more than 15 degrees
 * of hue, which is what keeps colour memory intact across the migration
 * (requirement 4: users navigate by "S3 is the orange one").
 *
 * Near-neutral colours are not identities: STS was `slate-300` (chroma 0.02,
 * below any ramp slot's), so it takes `fg-muted` rather than being pushed onto
 * a hue it never had.
 *
 * 35 services over 10 slots means slots are shared, deliberately (requirement
 * 5). The sharing that falls out is the sharing users already see — services
 * that shared a Tailwind hue share a slot now (ECR/EventBridge/Secrets Manager
 * were all reds; Pipes/Kinesis were both cyans). Colour is never the only
 * carrier: every surface that shows a service colour shows its icon and label
 * beside it (requirement 6).
 */
export const SERVICES = {
  // ── Storage & Database ─────────────────────────────────────────────────
  s3: {
    label: "S3",
    icon: Archive,
    color: "text-cat-2",
    bg: "bg-cat-2/10",
    border: "border-cat-2/30",
    css: "var(--cat-2)",
    letter: "S",
    to: "/s3",
    category: "storage",
    description: "Object storage",
    dashboardDescription: "Object storage — buckets, upload, download, and browse files.",
    docKey: "s3",
  },
  efs: {
    label: "EFS",
    icon: HardDrive,
    color: "text-cat-5",
    bg: "bg-cat-5/10",
    border: "border-cat-5/30",
    css: "var(--cat-5)",
    letter: "Ef",
    to: "/efs",
    category: "storage",
    description: "Elastic file systems",
    dashboardDescription:
      "Elastic file systems — file systems, mount targets, and access points (control plane).",
    docKey: "efs",
  },
  dynamodb: {
    label: "DynamoDB",
    icon: Database,
    color: "text-cat-7",
    bg: "bg-cat-7/10",
    border: "border-cat-7/30",
    css: "var(--cat-7)",
    letter: "D",
    to: "/dynamodb",
    category: "storage",
    description: "NoSQL key-value database",
    dashboardDescription: "NoSQL tables — manage tables, browse items, and run queries.",
    docKey: "dynamodb",
  },
  rds: {
    label: "RDS",
    icon: DatabaseZap,
    color: "text-cat-8",
    bg: "bg-cat-8/10",
    border: "border-cat-8/30",
    css: "var(--cat-8)",
    letter: "R",
    to: "/rds",
    category: "storage",
    description: "Relational databases",
    dashboardLabel: "RDS / Aurora",
    dashboardDescription: "Managed relational databases — MySQL, PostgreSQL, and Aurora.",
    docKey: "rds",
  },
  elasticache: {
    label: "ElastiCache",
    icon: DatabaseZap,
    color: "text-cat-4",
    bg: "bg-cat-4/10",
    border: "border-cat-4/30",
    css: "var(--cat-4)",
    letter: "EC",
    to: "/elasticache",
    category: "storage",
    description: "In-memory caching (Redis/Memcached)",
    dashboardDescription: "In-memory caching — Redis and Memcached clusters.",
    docKey: "elasticache",
  },
  msk: {
    label: "MSK",
    icon: Radio,
    color: "text-cat-7",
    bg: "bg-cat-7/10",
    border: "border-cat-7/30",
    css: "var(--cat-7)",
    letter: "MSK",
    to: "/msk",
    category: "messaging",
    description: "Managed Kafka clusters (Redpanda)",
    dashboardDescription: "Managed Kafka — clusters, bootstrap brokers, and configurations.",
    docKey: "msk",
  },

  // ── Analytics ──────────────────────────────────────────────────────────
  // Athena and the Glue Data Catalog share the violet-magenta end of the
  // ramp, as one query-and-catalog family; S3 Tables takes S3's slot, since
  // its tables live in S3 warehouse buckets. Their console pages are still to
  // come (#2072, #2086, #2087): until each lands, its routes render
  // ConsolePendingPage, and it adds its sub-nav `children` with its tabs.
  athena: {
    label: "Athena",
    icon: DatabaseSearch,
    color: "text-cat-8",
    bg: "bg-cat-8/10",
    border: "border-cat-8/30",
    css: "var(--cat-8)",
    letter: "At",
    to: "/athena",
    category: "analytics",
    description: "SQL queries over S3 data",
    dashboardDescription:
      "SQL over S3 data — run queries, then review history, saved queries and workgroups.",
    docKey: "athena",
  },
  glue: {
    label: "Glue",
    icon: LibraryBig,
    color: "text-cat-9",
    bg: "bg-cat-9/10",
    border: "border-cat-9/30",
    css: "var(--cat-9)",
    letter: "Gl",
    to: "/glue",
    category: "analytics",
    description: "Databases, tables and partitions",
    dashboardLabel: "Glue Data Catalog",
    dashboardDescription:
      "Data Catalog — the databases, tables and partitions Athena and Iceberg clients read.",
    docKey: "glue",
  },
  s3tables: {
    label: "S3 Tables",
    icon: Table2,
    color: "text-cat-2",
    bg: "bg-cat-2/10",
    border: "border-cat-2/30",
    css: "var(--cat-2)",
    letter: "S3T",
    to: "/s3tables",
    category: "analytics",
    description: "Managed Iceberg tables",
    dashboardDescription:
      "Iceberg tables in table buckets — namespaces, tables, snapshots and the REST catalog.",
    docKey: "s3tables",
  },

  // ── Compute ────────────────────────────────────────────────────────────
  lambda: {
    label: "Lambda",
    icon: Zap,
    color: "text-cat-9",
    bg: "bg-cat-9/10",
    border: "border-cat-9/30",
    css: "var(--cat-9)",
    letter: "λ",
    to: "/lambda",
    category: "compute",
    description: "Serverless functions",
    dashboardDescription: "Serverless functions — deploy, invoke, and view logs.",
    docKey: "lambda",
    children: [
      { to: "/lambda", label: "Functions" },
      { to: "/lambda/layers", label: "Layers" },
    ],
  },
  ec2: {
    label: "EC2",
    icon: Cpu,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "C",
    to: "/ec2",
    category: "compute",
    description: "Virtual machines",
    dashboardLabel: "EC2 / VPC",
    dashboardDescription: "Virtual machines and networking — instances, VPCs, and subnets.",
    docKey: "ec2",
  },
  ecs: {
    label: "ECS",
    icon: Boxes,
    color: "text-cat-5",
    bg: "bg-cat-5/10",
    border: "border-cat-5/30",
    css: "var(--cat-5)",
    letter: "E",
    to: "/ecs",
    category: "compute",
    description: "Container orchestration",
    dashboardDescription: "Container orchestration — clusters, task definitions, and services.",
    docKey: "ecs",
  },
  ecr: {
    label: "ECR",
    icon: Boxes,
    color: "text-cat-1",
    bg: "bg-cat-1/10",
    border: "border-cat-1/30",
    css: "var(--cat-1)",
    letter: "R",
    to: "/ecr",
    category: "compute",
    description: "Container image registry",
    dashboardDescription:
      "Container registry — repositories, image tags, digests, and local docker login hints.",
    docKey: "ecr",
  },
  eks: {
    label: "EKS",
    icon: Boxes,
    color: "text-cat-4",
    bg: "bg-cat-4/10",
    border: "border-cat-4/30",
    css: "var(--cat-4)",
    letter: "K8s",
    to: "/eks",
    category: "compute",
    description: "Managed Kubernetes control plane",
    dashboardDescription:
      "Kubernetes clusters — inspect control-plane metadata and cluster status.",
    docKey: "eks",
  },
  autoscaling: {
    label: "Auto Scaling",
    icon: Gauge,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "AS",
    to: "/autoscaling",
    category: "compute",
    description: "EC2 capacity that converges",
    dashboardDescription:
      "Auto Scaling groups — desired capacity, the instances actually running, policies and lifecycle hooks.",
    docKey: "autoscaling",
  },
  stepfunctions: {
    label: "Step Functions",
    icon: Workflow,
    color: "text-cat-5",
    bg: "bg-cat-5/10",
    border: "border-cat-5/30",
    css: "var(--cat-5)",
    letter: "W",
    to: "/stepfunctions",
    category: "compute",
    description: "State machine orchestration",
    dashboardDescription: "Serverless workflows — state machines and visual orchestration.",
    docKey: "stepfunctions",
  },

  // ── Messaging ──────────────────────────────────────────────────────────
  sqs: {
    label: "SQS",
    icon: MessagesSquare,
    color: "text-cat-3",
    bg: "bg-cat-3/10",
    border: "border-cat-3/30",
    css: "var(--cat-3)",
    letter: "Q",
    to: "/sqs",
    category: "messaging",
    description: "Message queues",
    dashboardDescription: "Message queues — send, receive, and inspect messages.",
    docKey: "sqs",
  },
  sns: {
    label: "SNS",
    icon: Bell,
    color: "text-cat-10",
    bg: "bg-cat-10/10",
    border: "border-cat-10/30",
    css: "var(--cat-10)",
    letter: "N",
    to: "/sns",
    category: "messaging",
    description: "Pub/sub notifications",
    dashboardDescription: "Pub/sub notifications — topics, subscriptions, and publishing.",
    docKey: "sns",
  },
  ses: {
    label: "SES",
    icon: Mail,
    color: "text-cat-2",
    bg: "bg-cat-2/10",
    border: "border-cat-2/30",
    css: "var(--cat-2)",
    letter: "M",
    to: "/ses",
    category: "messaging",
    description: "Email sending service",
    dashboardDescription:
      "Email identities — verify addresses and domains; mail lands in the Inbox.",
    docKey: "ses",
  },
  pipes: {
    label: "Pipes",
    icon: Cable,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "P",
    to: "/pipes",
    category: "messaging",
    description: "Event-driven pipelines",
    dashboardDescription: "EventBridge Pipes — route DynamoDB stream events to SQS queues.",
    docKey: "pipes",
  },
  kinesis: {
    label: "Kinesis",
    icon: Waves,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "K",
    to: "/kinesis",
    category: "messaging",
    description: "Real-time data streams",
    dashboardDescription: "Real-time data streams — create, manage, and inspect Kinesis streams.",
    docKey: "kinesis",
  },
  eventbridge: {
    label: "EventBridge",
    icon: Waypoints,
    color: "text-cat-1",
    bg: "bg-cat-1/10",
    border: "border-cat-1/30",
    css: "var(--cat-1)",
    letter: "Ev",
    to: "/eventbridge",
    category: "messaging",
    description: "Event bus",
    dashboardDescription: "Event bus — rules, targets, and event routing.",
    docKey: "eventbridge",
    nav: false,
  },

  // ── Security & Identity ────────────────────────────────────────────────
  secretsmanager: {
    label: "Secrets Manager",
    icon: KeyRound,
    color: "text-cat-1",
    bg: "bg-cat-1/10",
    border: "border-cat-1/30",
    css: "var(--cat-1)",
    letter: "Sm",
    to: "/secretsmanager",
    category: "security",
    description: "Secrets storage & rotation",
    dashboardDescription: "Secrets — create, retrieve, rotate, and manage secrets.",
    docKey: "secretsmanager",
  },
  iam: {
    label: "IAM",
    icon: Users,
    color: "text-cat-3",
    bg: "bg-cat-3/10",
    border: "border-cat-3/30",
    css: "var(--cat-3)",
    letter: "I",
    to: "/iam",
    category: "security",
    description: "Identity & access management",
    dashboardDescription: "Identity and Access Management — roles, users, and policies.",
    docKey: "iam",
  },
  cognito: {
    label: "Cognito",
    icon: UserCheck,
    color: "text-cat-8",
    bg: "bg-cat-8/10",
    border: "border-cat-8/30",
    css: "var(--cat-8)",
    letter: "U",
    to: "/cognito",
    category: "security",
    description: "User authentication & pools",
    dashboardDescription: "User authentication — user pools and identity providers.",
    docKey: "cognito",
  },
  kms: {
    label: "KMS",
    icon: Key,
    color: "text-cat-3",
    bg: "bg-cat-3/10",
    border: "border-cat-3/30",
    css: "var(--cat-3)",
    letter: "Km",
    to: "/kms",
    category: "security",
    description: "Encryption key management",
    dashboardLabel: "KMS",
    dashboardDescription:
      "Key Management Service — encryption keys, aliases, and crypto operations.",
    docKey: "kms",
    nav: false,
  },
  ssm: {
    label: "SSM",
    icon: SlidersHorizontal,
    color: "text-cat-2",
    bg: "bg-cat-2/10",
    border: "border-cat-2/30",
    css: "var(--cat-2)",
    letter: "Ss",
    to: "/ssm",
    category: "security",
    description: "Parameter store",
    dashboardLabel: "SSM Parameter Store",
    dashboardDescription:
      "Systems Manager — parameter store for config, secrets, and feature flags.",
    docKey: "ssm",
    nav: false,
  },
  sts: {
    label: "STS",
    icon: Fingerprint,
    color: "text-fg-muted",
    bg: "bg-fg-muted/10",
    border: "border-fg-muted/30",
    css: "var(--fg-muted)",
    letter: "St",
    to: "/sts",
    category: "security",
    description: "Temporary credentials",
    dashboardDescription: "Security Token Service — temporary credentials and caller identity.",
    docKey: "sts",
    nav: false,
  },
  waf: {
    label: "WAF",
    icon: ShieldAlert,
    color: "text-cat-1",
    bg: "bg-cat-1/10",
    border: "border-cat-1/30",
    css: "var(--cat-1)",
    letter: "Wf",
    to: "/waf",
    category: "security",
    description: "Web application firewall",
    dashboardDescription:
      "Metadata-only Web ACL CRUD for SDK and CloudFormation workflows; rules are not enforced.",
    docKey: "waf",
  },
  shield: {
    label: "Shield",
    icon: ShieldCheck,
    color: "text-cat-8",
    bg: "bg-cat-8/10",
    border: "border-cat-8/30",
    css: "var(--cat-8)",
    letter: "Sh",
    to: "/shield",
    category: "security",
    description: "DDoS protection",
    favouritable: false,
    dashboardCard: false,
  },

  // ── Networking & APIs ──────────────────────────────────────────────────
  apigateway: {
    label: "API Gateway",
    icon: PlugZap,
    color: "text-cat-4",
    bg: "bg-cat-4/10",
    border: "border-cat-4/30",
    css: "var(--cat-4)",
    letter: "A",
    to: "/apigateway",
    category: "networking",
    description: "REST & WebSocket APIs",
    dashboardDescription: "HTTP and REST APIs — create, deploy, and manage endpoints.",
    docKey: "apigateway",
    children: [
      { to: "/apigateway", label: "APIs" },
      { to: "/apigateway/api-keys", label: "API Keys" },
      { to: "/apigateway/usage-plans", label: "Usage Plans" },
    ],
  },
  cloudfront: {
    label: "CloudFront",
    icon: Globe,
    color: "text-cat-9",
    bg: "bg-cat-9/10",
    border: "border-cat-9/30",
    css: "var(--cat-9)",
    letter: "Cf",
    to: "/cloudfront",
    category: "networking",
    description: "Content delivery network",
    dashboardDescription: "Content delivery network — distributions and edge caching.",
    docKey: "cloudfront",
    children: [
      { to: "/cloudfront", label: "Distributions" },
      { to: "/cloudfront/continuous-deployment-policies", label: "Continuous Deployment" },
      {
        group: "Security",
        items: [
          { to: "/cloudfront/key-groups", label: "Key Groups" },
          { to: "/cloudfront/fle-configs", label: "FLE Configs" },
          { to: "/cloudfront/fle-profiles", label: "FLE Profiles" },
        ],
      },
      {
        group: "Logging",
        items: [{ to: "/cloudfront/realtime-log-configs", label: "Realtime Log Configs" }],
      },
    ],
  },
  appsync: {
    label: "AppSync",
    icon: Braces,
    color: "text-cat-10",
    bg: "bg-cat-10/10",
    border: "border-cat-10/30",
    css: "var(--cat-10)",
    letter: "As",
    to: "/appsync",
    category: "networking",
    description: "Managed GraphQL API",
    dashboardDescription: "Managed GraphQL — APIs, resolvers, and data sources.",
    docKey: "appsync",
  },
  cloudformation: {
    label: "CloudFormation",
    icon: Layers,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "CF",
    to: "/cloudformation/",
    category: "networking",
    description: "Infrastructure as code",
    dashboardDescription: "Infrastructure as code — deploy and manage stacks.",
    docKey: "cloudformation",
  },
  appregistry: {
    label: "Applications",
    icon: Boxes,
    color: "text-cat-6",
    bg: "bg-cat-6/10",
    border: "border-cat-6/30",
    css: "var(--cat-6)",
    letter: "App",
    to: "/applications/",
    category: "networking",
    description: "AppRegistry — resource groupings",
    dashboardCard: false,
  },

  // ── Monitoring ─────────────────────────────────────────────────────────
  cloudwatch: {
    label: "CloudWatch",
    icon: Activity,
    color: "text-cat-4",
    bg: "bg-cat-4/10",
    border: "border-cat-4/30",
    css: "var(--cat-4)",
    letter: "CW",
    to: "/cloudwatch",
    category: "monitoring",
    description: "Metrics, alarms & logs",
    dashboardCard: false,
    children: [
      { to: "/cloudwatch/logs", label: "Logs" },
      { to: "/cloudwatch", label: "Metrics" },
    ],
  },

  // ── Topology / map-only (no route) ─────────────────────────────────────
  /**
   * "logs" is used by the topology map and as the dashboard card for
   * CloudWatch Logs. It deliberately points to /cloudwatch/logs rather
   * than having its own top-level route.
   */
  logs: {
    label: "CloudWatch Logs",
    icon: ScrollText,
    color: "text-cat-5",
    bg: "bg-cat-5/10",
    border: "border-cat-5/30",
    css: "var(--cat-5)",
    letter: "L",
    to: "/cloudwatch/logs",
    category: "monitoring",
    description: "Log groups and streams",
    dashboardLabel: "CloudWatch",
    dashboardDescription: "Observability — logs, metrics, and alarms.",
    docKey: "cloudwatch-logs",
    nav: false,
  },
  vpc: {
    label: "VPC",
    icon: Network,
    color: "text-cat-5",
    bg: "bg-cat-5/10",
    border: "border-cat-5/30",
    css: "var(--cat-5)",
    letter: "V",
  },
  igw: {
    label: "Internet Gateway",
    icon: Globe,
    color: "text-cat-7",
    bg: "bg-cat-7/10",
    border: "border-cat-7/30",
    css: "var(--cat-7)",
    letter: "IG",
  },
} as const satisfies Record<string, ServiceEntry>

// ── Route matching ─────────────────────────────────────────────────────────

/**
 * Whether `pathname` is owned by a service whose primary route is `to`.
 * Segment-aware, and tolerant of the trailing slash a few entries carry, so
 * "/ec2" never claims "/ec2-classic" and "/cloudformation/" still owns
 * "/cloudformation".
 */
export function matchesRoute(pathname: string, to: string): boolean {
  const base = to.replace(/\/+$/, "")
  return pathname === base || pathname.startsWith(base + "/")
}

/** Routed entries, longest route first, so the most specific one matches. */
const ROUTED_SERVICES = Object.values(SERVICES as Record<string, ServiceEntry>)
  .filter((entry): entry is ServiceEntry & { to: string } => entry.to != null)
  .sort((a, b) => b.to.length - a.to.length)

/**
 * The service that owns `pathname`, across the whole registry rather than the
 * nav subset — dashboard-only services (KMS, SSM, STS) own their routes too.
 * Undefined on the dashboard and on non-service pages (map, docs, metrics).
 */
export function findServiceForPathname(pathname: string): ServiceEntry | undefined {
  if (pathname === "/") return undefined
  return ROUTED_SERVICES.find((entry) => matchesRoute(pathname, entry.to))
}
