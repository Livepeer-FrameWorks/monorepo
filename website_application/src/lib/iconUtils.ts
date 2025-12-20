import type { ComponentType } from "svelte";
import {
  Activity,
  AlertCircle,
  AlertTriangle,
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  BarChart3,
  BarChart,
  Bell,
  Bot,
  BookOpen,
  Brain,
  Building2,
  Calendar,
  Camera,
  CheckCircle,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  ChartLine,
  Circle,
  CircleDot,
  CircleSlash,
  Clapperboard,
  Clock,
  Code2,
  Copy,
  CreditCard,
  Database,
  Edit,
  ExternalLink,
  Download,
  FileText,
  Film,
  Filter,
  FolderOpen,
  Gauge,
  Globe,
  Globe2,
  HardDrive,
  HelpCircle,
  Home,
  Info,
  Key,
  LayoutDashboard,
  LayoutGrid,
  Lightbulb,
  Link,
  Loader,
  Loader2,
  LogIn,
  Mail,
  Maximize2,
  Mic,
  MessageCircle,
  Minimize2,
  Monitor,
  Network,
  Package,
  PauseCircle,
  Play,
  Plus,
  Radio,
  Receipt,
  RefreshCw,
  Rocket,
  Scissors,
  ScrollText,
  Search,
  Server,
  Settings,
  Share2,
  Signal,
  Square,
  Shield,
  Sparkles,
  StopCircle,
  Target,
  Ticket,
  Trash2,
  TrendingUp,
  Upload,
  User,
  UserPlus,
  Users,
  Video,
  CircleOff,
  Wifi,
  WifiOff,
  Wrench,
  X,
  XCircle,
  Zap,
} from "lucide-svelte";

// Map of icon names to components
const iconMap = {
  Activity,
  AlertCircle,
  AlertTriangle,
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  BarChart3,
  BarChart,
  Bell,
  Bot,
  BookOpen,
  Brain,
  Building2,
  Calendar,
  Camera,
  CheckCircle,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  ChartLine,
  Circle,
  CircleDot,
  CircleSlash,
  Clapperboard,
  Clock,
  Code2,
  Copy,
  CreditCard,
  Database,
  Edit,
  ExternalLink,
  Download,
  FileText,
  Film,
  Filter,
  FolderOpen,
  Gauge,
  Globe,
  Globe2,
  HardDrive,
  HelpCircle,
  Home,
  Info,
  Key,
  LayoutDashboard,
  LayoutGrid,
  Lightbulb,
  Link,
  Loader,
  Loader2,
  LogIn,
  Mail,
  Maximize2,
  Mic,
  MessageCircle,
  Minimize2,
  Monitor,
  Network,
  Package,
  PauseCircle,
  Play,
  Plus,
  Radio,
  Receipt,
  RefreshCw,
  Rocket,
  Scissors,
  ScrollText,
  Search,
  Server,
  Settings,
  Share2,
  Signal,
  Square,
  Shield,
  Sparkles,
  StopCircle,
  Target,
  Ticket,
  Trash2,
  TrendingUp,
  Upload,
  User,
  UserPlus,
  Users,
  Video,
  CircleOff,
  Wifi,
  WifiOff,
  Wrench,
  X,
  XCircle,
  Zap,
};

export type IconName =
  | "Activity"
  | "AlertCircle"
  | "AlertTriangle"
  | "ArrowDown"
  | "ArrowLeft"
  | "ArrowUp"
  | "BarChart3"
  | "BarChart"
  | "Bell"
  | "Bot"
  | "BookOpen"
  | "Brain"
  | "Building2"
  | "Calendar"
  | "Camera"
  | "CheckCircle"
  | "ChevronDown"
  | "ChevronLeft"
  | "ChevronRight"
  | "ChevronUp"
  | "ChartLine"
  | "Circle"
  | "CircleDot"
  | "CircleSlash"
  | "Clapperboard"
  | "Clock"
  | "Code2"
  | "Copy"
  | "CreditCard"
  | "Database"
  | "Edit"
  | "ExternalLink"
  | "Download"
  | "FileText"
  | "Film"
  | "Filter"
  | "FolderOpen"
  | "Gauge"
  | "Globe"
  | "Globe2"
  | "HardDrive"
  | "HelpCircle"
  | "Home"
  | "Info"
  | "Key"
  | "LayoutDashboard"
  | "LayoutGrid"
  | "Lightbulb"
  | "Link"
  | "Loader"
  | "Loader2"
  | "LogIn"
  | "Mail"
  | "Maximize2"
  | "Mic"
  | "MessageCircle"
  | "Minimize2"
  | "Monitor"
  | "Network"
  | "Package"
  | "PauseCircle"
  | "Play"
  | "Plus"
  | "Radio"
  | "Receipt"
  | "RefreshCw"
  | "Rocket"
  | "Scissors"
  | "ScrollText"
  | "Search"
  | "Server"
  | "Settings"
  | "Share2"
  | "Signal"
  | "Square"
  | "Shield"
  | "Sparkles"
  | "StopCircle"
  | "Target"
  | "Ticket"
  | "Trash2"
  | "TrendingUp"
  | "Upload"
  | "User"
  | "UserPlus"
  | "Users"
  | "Video"
  | "CircleOff"
  | "Wifi"
  | "WifiOff"
  | "Wrench"
  | "X"
  | "XCircle"
  | "Zap";

export function getIconComponent(iconName?: string | null): ComponentType {
  if (!iconName) return HelpCircle;
  return iconMap[iconName as IconName] || HelpCircle;
}
