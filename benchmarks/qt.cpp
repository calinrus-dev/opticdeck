#include <QApplication>
#include <QCheckBox>
#include <QLabel>
#include <QLineEdit>
#include <QPushButton>
#include <QVBoxLayout>
#include <QWidget>

int main(int argc, char** argv) {
    QApplication app(argc, argv);
    QApplication::setStyle("Fusion");
    QWidget window;
    window.setWindowTitle("OpticDeck benchmark · Qt");
    window.resize(640, 420);

    auto* layout = new QVBoxLayout(&window);
    layout->setContentsMargins(28, 28, 28, 28);
    layout->setSpacing(14);

    auto* project = new QLineEdit("Original project");
    project->setAccessibleName("Project name");
    auto* include = new QCheckBox("Include draft");
    auto* apply = new QPushButton("Apply changes");
    auto* result = new QLabel("Pending");

    layout->addWidget(new QLabel("Qt benchmark"));
    layout->addWidget(project);
    layout->addWidget(include);
    layout->addWidget(apply);
    layout->addWidget(result);
    layout->addStretch();

    QObject::connect(apply, &QPushButton::clicked, &window, [=] {
        const auto suffix = include->isChecked() ? "draft included" : "draft excluded";
        result->setText(QString("Saved: %1 (%2)").arg(project->text(), suffix));
    });

    window.show();
    return app.exec();
}
